// Package signer provides a factory for creating cosmos-sdk keyrings
// from various backends (local filesystem, POPSigner, AWS KMS, etc.).
//
// Usage:
//
//	kr, keyName, err := signer.NewKeyring(cfg)
//	if err != nil {
//	    return err
//	}
//	// Use kr with celestia-node's txClient
package signer
