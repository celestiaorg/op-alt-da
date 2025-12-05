package sdkconfig

import (
	"sync"

	"github.com/celestiaorg/celestia-app/v6/app/params"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

var once sync.Once

// InitCelestiaPrefix configures the Cosmos SDK to use Celestia's Bech32 prefix
// This must be called before any address operations. It's safe to call multiple times.
// If config is already sealed (e.g., by celestia-app's init), this function will silently succeed.
func InitCelestiaPrefix() {
	once.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				// Config already sealed by celestia-app or another package. That's OK.
			}
		}()
		config := sdk.GetConfig()
		config.SetBech32PrefixForAccount(params.Bech32PrefixAccAddr, params.Bech32PrefixAccPub)
	})
}
