package celestia

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	secp256k1x509 "github.com/MetaMask/go-did-it/crypto/secp256k1"
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
	secp256k1ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

var (
	errUnsupportedKMSOp = errors.New("kms keyring: operation not supported")
)

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

// hexToPrivateKey converts hex-encoded private key to PKCS#8 DER format
func hexToPrivateKey(privKeyHex string) ([]byte, error) {
	// Strip 0x prefix if present
	privKeyHex = strings.TrimPrefix(privKeyHex, "0x")

	// Decode hex to bytes
	privKeyBytes, err := hex.DecodeString(privKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode hex: %w", err)
	}

	// Validate length (32 bytes for secp256k1)
	if len(privKeyBytes) != 32 {
		return nil, fmt.Errorf("invalid key length: got %d bytes, expected 32", len(privKeyBytes))
	}

	// Convert to MetaMask PrivateKey
	privKey, err := secp256k1x509.PrivateKeyFromBytes(privKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("create private key: %w", err)
	}

	// Convert to PKCS#8 DER format
	return privKey.ToPKCS8DER(), nil
}

// wrapKeyMaterial encrypts key material for KMS import
func wrapKeyMaterial(keyMaterial []byte, wrappingKey []byte) ([]byte, error) {
	// Parse RSA public key from wrapping key
	pubKeyInterface, err := x509.ParsePKIXPublicKey(wrappingKey)
	if err != nil {
		return nil, fmt.Errorf("parse wrapping key: %w", err)
	}

	rsaPubKey, ok := pubKeyInterface.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("wrapping key is not RSA")
	}

	// Encrypt using RSA-OAEP with SHA-256
	encrypted, err := rsa.EncryptOAEP(
		sha256.New(),
		rand.Reader,
		rsaPubKey,
		keyMaterial,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("encrypt key material: %w", err)
	}

	return encrypted, nil
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

	// Handle key import if specified in config
	if cfg.ImportKeyName != "" && cfg.ImportKeyHex != "" {
		// Check if key already exists
		_, err := k.getRecord(cfg.ImportKeyName)
		if err == nil {
			// Key exists, skip import (idempotent behavior)
		} else if errors.Is(err, sdkerrors.ErrKeyNotFound) {
			// Key doesn't exist, import it
			if err := k.ImportPrivKeyHex(cfg.ImportKeyName, cfg.ImportKeyHex, "secp256k1"); err != nil {
				return nil, fmt.Errorf("import key %q: %w", cfg.ImportKeyName, err)
			}
		} else {
			// Other error occurred while checking key existence
			return nil, fmt.Errorf("check key existence %q: %w", cfg.ImportKeyName, err)
		}
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

func kmsPubKeyToCosmos(derBytes []byte) (cryptotypes.PubKey, error) {
	pub, err := secp256k1x509.PublicKeyFromX509DER(derBytes)
	if err != nil {
		return nil, err
	}

	// ToBytes() returns 33-byte compressed format
	return &secp256k1.PubKey{Key: pub.ToBytes()}, nil
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

func (k *kmsKeyring) NewMnemonic(uid string, language keyring.Language, hdPath, bip39Passphrase string, algo keyring.SignatureAlgo) (*keyring.Record, string, error) {
	// Validate algorithm
	if algo != hd.Secp256k1 {
		return nil, "", fmt.Errorf("kms: only secp256k1 supported, got %s", algo)
	}

	// Create key in KMS
	createResp, err := k.client.CreateKey(k.ctx, &kms.CreateKeyInput{
		KeySpec:     kmstypes.KeySpecEccSecgP256k1,
		KeyUsage:    kmstypes.KeyUsageTypeSignVerify,
		Description: aws.String(fmt.Sprintf("Celestia account: %s", uid)),
	})
	if err != nil {
		return nil, "", fmt.Errorf("kms create key: %w", err)
	}

	// Create alias
	aliasName := k.aliasPrefix + uid
	_, err = k.client.CreateAlias(k.ctx, &kms.CreateAliasInput{
		AliasName:   aws.String(aliasName),
		TargetKeyId: createResp.KeyMetadata.KeyId,
	})
	if err != nil {
		return nil, "", fmt.Errorf("kms create alias: %w", err)
	}

	// Refresh cache to pick up new key
	if err := k.refreshCache(); err != nil {
		return nil, "", err
	}

	// Get the record
	rec, err := k.Key(uid)
	if err != nil {
		return nil, "", err
	}

	// Return empty mnemonic (not applicable for KMS-managed keys)
	return rec, "", nil
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

// derSignatureToSecp converts a DER-encoded ECDSA signature from AWS KMS to raw secp256k1 format (64 bytes: R || S).
// It uses github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa for robust parsing and automatic low-S normalization.
func derSignatureToSecp(der []byte) ([]byte, error) {
	// Parse DER signature using the battle-tested decred library
	sig, err := secp256k1ecdsa.ParseDERSignature(der)
	if err != nil {
		return nil, fmt.Errorf("parse der signature: %w", err)
	}

	// Extract R and S as 32-byte arrays (automatically normalized to low-S by ParseDERSignature)
	r := sig.R()
	s := sig.S()
	rBytes := r.Bytes()
	sBytes := s.Bytes()

	// Concatenate R and S to create the 64-byte signature format expected by Cosmos
	result := make([]byte, 0, 64)
	result = append(result, rBytes[:]...)
	result = append(result, sBytes[:]...)

	return result, nil
}

func (k *kmsKeyring) ImportPrivKey(uid string, armor string, passphrase string) error {
	return fmt.Errorf("kms: armored import not supported, use ImportPrivKeyHex for hex-encoded keys")
}

func (k *kmsKeyring) ImportPrivKeyHex(uid string, privKeyHex string, algoStr string) error {
	// Validate algorithm
	if algoStr != "secp256k1" {
		return fmt.Errorf("kms: only secp256k1 supported, got %s", algoStr)
	}

	// Convert hex to PKCS#8 DER
	keyMaterial, err := hexToPrivateKey(privKeyHex)
	if err != nil {
		return fmt.Errorf("convert private key: %w", err)
	}

	// Step 1: Create KMS key with EXTERNAL origin
	createResp, err := k.client.CreateKey(k.ctx, &kms.CreateKeyInput{
		Origin:      kmstypes.OriginTypeExternal,
		KeySpec:     kmstypes.KeySpecEccSecgP256k1,
		KeyUsage:    kmstypes.KeyUsageTypeSignVerify,
		Description: aws.String(fmt.Sprintf("Celestia imported account: %s", uid)),
	})
	if err != nil {
		return fmt.Errorf("kms create key: %w", err)
	}
	keyID := aws.ToString(createResp.KeyMetadata.KeyId)

	// Step 2: Get wrapping parameters
	paramsResp, err := k.client.GetParametersForImport(k.ctx, &kms.GetParametersForImportInput{
		KeyId:             aws.String(keyID),
		WrappingAlgorithm: kmstypes.AlgorithmSpecRsaesOaepSha256,
		WrappingKeySpec:   kmstypes.WrappingKeySpecRsa2048,
	})
	if err != nil {
		return fmt.Errorf("kms get import parameters: %w", err)
	}

	// Step 3: Wrap key material
	encryptedMaterial, err := wrapKeyMaterial(keyMaterial, paramsResp.PublicKey)
	if err != nil {
		return fmt.Errorf("wrap key material: %w", err)
	}

	// Step 4: Import key material
	_, err = k.client.ImportKeyMaterial(k.ctx, &kms.ImportKeyMaterialInput{
		KeyId:                aws.String(keyID),
		ImportToken:          paramsResp.ImportToken,
		EncryptedKeyMaterial: encryptedMaterial,
		ExpirationModel:      kmstypes.ExpirationModelTypeKeyMaterialDoesNotExpire,
	})
	if err != nil {
		// Clean up the key if import fails
		_, _ = k.client.ScheduleKeyDeletion(k.ctx, &kms.ScheduleKeyDeletionInput{
			KeyId:               aws.String(keyID),
			PendingWindowInDays: aws.Int32(7),
		})
		return fmt.Errorf("kms import key material: %w", err)
	}

	// Step 5: Create alias
	aliasName := k.aliasPrefix + uid
	_, err = k.client.CreateAlias(k.ctx, &kms.CreateAliasInput{
		AliasName:   aws.String(aliasName),
		TargetKeyId: aws.String(keyID),
	})
	if err != nil {
		return fmt.Errorf("kms create alias: %w", err)
	}

	// Step 6: Refresh cache
	if err := k.refreshCache(); err != nil {
		return err
	}

	return nil
}
func (k *kmsKeyring) ImportPubKey(string, string) error        { return errUnsupportedKMSOp }
func (k *kmsKeyring) ExportPubKeyArmor(string) (string, error) { return "", errUnsupportedKMSOp }
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
