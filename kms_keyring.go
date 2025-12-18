package celestia

import (
	"context"
	"crypto/ecdsa"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	errUnsupportedKMSOp    = errors.New("kms keyring: operation not supported")
	oidPublicKeyECDSA      = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidNamedCurveSecp256k1 = asn1.ObjectIdentifier{1, 3, 132, 0, 10}
)

type subjectPublicKeyInfo struct {
	Algorithm        pkixAlgorithmIdentifier
	SubjectPublicKey asn1.BitString
}

type pkixAlgorithmIdentifier struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type kmsKeyring struct {
	ctx         context.Context
	client      *kms.Client
	aliasPrefix string

	mu         sync.RWMutex
	records    map[string]*kmsCachedRecord
	addrLookup map[string]string
}

type kmsCachedRecord struct {
	keyID string
	rec   *keyring.Record
	pub   cryptotypes.PubKey
}

func newKMSKeyring(ctx context.Context, defaultKey string, cfg KMSConfig) (keyring.Keyring, error) {
	if cfg.Region == "" {
		return nil, fmt.Errorf("kms region is required")
	}
	aliasPrefix := cfg.AliasPrefix
	if aliasPrefix == "" {
		aliasPrefix = "alias/op-alt-da/"
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.Endpoint != "" {
		resolver := aws.EndpointResolverWithOptionsFunc(
			func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				if service == kms.ServiceID {
					return aws.Endpoint{URL: cfg.Endpoint, HostnameImmutable: true}, nil
				}
				return aws.Endpoint{}, &aws.EndpointNotFoundError{}
			},
		)
		loadOpts = append(loadOpts, awsconfig.WithEndpointResolverWithOptions(resolver))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	k := &kmsKeyring{
		ctx:         ctx,
		client:      kms.NewFromConfig(awsCfg),
		aliasPrefix: aliasPrefix,
		records:     make(map[string]*kmsCachedRecord),
		addrLookup:  make(map[string]string),
	}

	if err := k.refreshCache(); err != nil {
		return nil, err
	}

	if defaultKey != "" {
		if _, err := k.getRecord(defaultKey); err != nil {
			return nil, fmt.Errorf("kms key %q not found: %w", defaultKey, err)
		}
	}

	return k, nil
}

func (k *kmsKeyring) refreshCache() error {
	aliases, err := k.listAliases()
	if err != nil {
		return err
	}

	newRecords := make(map[string]*kmsCachedRecord)
	newAddrIndex := make(map[string]string)

	for _, alias := range aliases {
		if alias.AliasName == nil || alias.TargetKeyId == nil {
			continue
		}
		aliasName := aws.ToString(alias.AliasName)
		if !strings.HasPrefix(aliasName, k.aliasPrefix) {
			continue
		}
		keyName := strings.TrimPrefix(aliasName, k.aliasPrefix)
		cached, err := k.buildRecord(keyName, aws.ToString(alias.TargetKeyId))
		if err != nil {
			return fmt.Errorf("build record %s: %w", keyName, err)
		}

		addr := sdk.AccAddress(cached.pub.Address()).String()
		newRecords[keyName] = cached
		newAddrIndex[addr] = keyName
	}

	k.mu.Lock()
	k.records = newRecords
	k.addrLookup = newAddrIndex
	k.mu.Unlock()

	return nil
}

func (k *kmsKeyring) listAliases() ([]kmstypes.AliasListEntry, error) {
	paginator := kms.NewListAliasesPaginator(k.client, &kms.ListAliasesInput{})
	var aliases []kmstypes.AliasListEntry
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(k.ctx)
		if err != nil {
			return nil, fmt.Errorf("list aliases: %w", err)
		}
		aliases = append(aliases, page.Aliases...)
	}
	return aliases, nil
}

func (k *kmsKeyring) buildRecord(name, keyID string) (*kmsCachedRecord, error) {
	pkResp, err := k.client.GetPublicKey(k.ctx, &kms.GetPublicKeyInput{KeyId: aws.String(keyID)})
	if err != nil {
		return nil, fmt.Errorf("get public key: %w", err)
	}

	pubKey, err := kmsPubKeyToCosmos(pkResp.PublicKey)
	if err != nil {
		return nil, err
	}

	anyPub, err := types.NewAnyWithValue(pubKey)
	if err != nil {
		return nil, fmt.Errorf("pack pubkey: %w", err)
	}

	rec := &keyring.Record{
		Name:   name,
		PubKey: anyPub,
		Item: &keyring.Record_Offline_{
			Offline: &keyring.Record_Offline{},
		},
	}

	return &kmsCachedRecord{keyID: keyID, rec: rec, pub: pubKey}, nil
}

func kmsPubKeyToCosmos(der []byte) (cryptotypes.PubKey, error) {
	ecdsaPub, err := parseSecp256k1PublicKey(der)
	if err != nil {
		return nil, err
	}

	compressed := crypto.CompressPubkey(ecdsaPub)
	pk := &secp256k1.PubKey{Key: compressed}
	return pk, nil
}

func parseSecp256k1PublicKey(der []byte) (*ecdsa.PublicKey, error) {
	var spki subjectPublicKeyInfo
	if _, err := asn1.Unmarshal(der, &spki); err != nil {
		return nil, fmt.Errorf("parse kms public key: %w", err)
	}

	if !spki.Algorithm.Algorithm.Equal(oidPublicKeyECDSA) {
		return nil, fmt.Errorf("kms key is not an ECDSA public key")
	}

	if len(spki.Algorithm.Parameters.FullBytes) == 0 {
		return nil, fmt.Errorf("kms key is missing curve parameters")
	}

	var namedCurveOID asn1.ObjectIdentifier
	if _, err := asn1.Unmarshal(spki.Algorithm.Parameters.FullBytes, &namedCurveOID); err != nil {
		return nil, fmt.Errorf("parse kms public key curve params: %w", err)
	}

	if !namedCurveOID.Equal(oidNamedCurveSecp256k1) {
		return nil, fmt.Errorf("kms key is not secp256k1")
	}

	if spki.SubjectPublicKey.BitLength%8 != 0 {
		return nil, fmt.Errorf("kms public key bit length is invalid")
	}

	pubKeyBytes := spki.SubjectPublicKey.RightAlign()
	if len(pubKeyBytes) == 0 {
		return nil, fmt.Errorf("kms public key is empty")
	}

	ecdsaPub, err := crypto.UnmarshalPubkey(pubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("unmarshal secp256k1 public key: %w", err)
	}

	return ecdsaPub, nil
}

func (k *kmsKeyring) Backend() string {
	return "kms"
}

func (k *kmsKeyring) List() ([]*keyring.Record, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	records := make([]*keyring.Record, 0, len(k.records))
	names := make([]string, 0, len(k.records))
	for name := range k.records {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		records = append(records, k.records[name].rec)
	}
	return records, nil
}

func (k *kmsKeyring) SupportedAlgorithms() (keyring.SigningAlgoList, keyring.SigningAlgoList) {
	algos := keyring.SigningAlgoList{hd.Secp256k1}
	return algos, algos
}

func (k *kmsKeyring) Key(uid string) (*keyring.Record, error) {
	cached, err := k.getRecord(uid)
	if err != nil {
		return nil, err
	}
	return cached.rec, nil
}

func (k *kmsKeyring) KeyByAddress(address sdk.Address) (*keyring.Record, error) {
	k.mu.RLock()
	name, ok := k.addrLookup[address.String()]
	k.mu.RUnlock()
	if !ok {
		return nil, sdkerrors.ErrKeyNotFound
	}
	return k.Key(name)
}

func (k *kmsKeyring) getRecord(name string) (*kmsCachedRecord, error) {
	k.mu.RLock()
	cached, ok := k.records[name]
	k.mu.RUnlock()
	if ok {
		return cached, nil
	}
	if err := k.refreshCache(); err != nil {
		return nil, err
	}
	k.mu.RLock()
	cached, ok = k.records[name]
	k.mu.RUnlock()
	if !ok {
		return nil, sdkerrors.ErrKeyNotFound
	}
	return cached, nil
}

func (k *kmsKeyring) Delete(string) error               { return errUnsupportedKMSOp }
func (k *kmsKeyring) DeleteByAddress(sdk.Address) error { return errUnsupportedKMSOp }
func (k *kmsKeyring) Rename(string, string) error       { return errUnsupportedKMSOp }
func (k *kmsKeyring) NewMnemonic(string, keyring.Language, string, string, keyring.SignatureAlgo) (*keyring.Record, string, error) {
	return nil, "", errUnsupportedKMSOp
}
func (k *kmsKeyring) NewAccount(string, string, string, string, keyring.SignatureAlgo) (*keyring.Record, error) {
	return nil, errUnsupportedKMSOp
}
func (k *kmsKeyring) SaveLedgerKey(string, keyring.SignatureAlgo, string, uint32, uint32, uint32) (*keyring.Record, error) {
	return nil, errUnsupportedKMSOp
}
func (k *kmsKeyring) SaveOfflineKey(string, cryptotypes.PubKey) (*keyring.Record, error) {
	return nil, errUnsupportedKMSOp
}
func (k *kmsKeyring) SaveMultisig(string, cryptotypes.PubKey) (*keyring.Record, error) {
	return nil, errUnsupportedKMSOp
}

func (k *kmsKeyring) Sign(uid string, msg []byte, signMode signing.SignMode) ([]byte, cryptotypes.PubKey, error) {
	cached, err := k.getRecord(uid)
	if err != nil {
		return nil, nil, err
	}

	sigResp, err := k.client.Sign(k.ctx, &kms.SignInput{
		KeyId:            aws.String(cached.keyID),
		Message:          msg,
		MessageType:      kmstypes.MessageTypeRaw,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecEcdsaSha256,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("kms sign: %w", err)
	}

	signature, err := derSignatureToSecp(sigResp.Signature)
	if err != nil {
		return nil, nil, err
	}

	return signature, cached.pub, nil
}

func (k *kmsKeyring) SignByAddress(address sdk.Address, msg []byte, signMode signing.SignMode) ([]byte, cryptotypes.PubKey, error) {
	k.mu.RLock()
	name, ok := k.addrLookup[address.String()]
	k.mu.RUnlock()
	if !ok {
		return nil, nil, sdkerrors.ErrKeyNotFound
	}
	return k.Sign(name, msg, signMode)
}

func derSignatureToSecp(der []byte) ([]byte, error) {
	var parsed struct {
		R, S *big.Int
	}
	if _, err := asn1.Unmarshal(der, &parsed); err != nil {
		return nil, fmt.Errorf("parse der signature: %w", err)
	}

	curveN := crypto.S256().Params().N
	halfOrder := new(big.Int).Rsh(new(big.Int).Set(curveN), 1)
	if parsed.S.Cmp(halfOrder) == 1 {
		parsed.S.Sub(curveN, parsed.S)
	}

	rBytes := parsed.R.FillBytes(make([]byte, 32))
	sBytes := parsed.S.FillBytes(make([]byte, 32))

	return append(rBytes, sBytes...), nil
}

func (k *kmsKeyring) ImportPrivKey(string, string, string) error    { return errUnsupportedKMSOp }
func (k *kmsKeyring) ImportPrivKeyHex(string, string, string) error { return errUnsupportedKMSOp }
func (k *kmsKeyring) ImportPubKey(string, string) error             { return errUnsupportedKMSOp }
func (k *kmsKeyring) ExportPubKeyArmor(string) (string, error)      { return "", errUnsupportedKMSOp }
func (k *kmsKeyring) ExportPubKeyArmorByAddress(sdk.Address) (string, error) {
	return "", errUnsupportedKMSOp
}
func (k *kmsKeyring) ExportPrivKeyArmor(string, string) (string, error) {
	return "", errUnsupportedKMSOp
}
func (k *kmsKeyring) ExportPrivKeyArmorByAddress(sdk.Address, string) (string, error) {
	return "", errUnsupportedKMSOp
}
func (k *kmsKeyring) MigrateAll() ([]*keyring.Record, error) { return nil, errUnsupportedKMSOp }
