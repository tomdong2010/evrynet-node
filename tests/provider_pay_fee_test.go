package tests

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/stretchr/testify/assert"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

/* These tests are done on a chain with already setup account/ contracts.
To run these test, please deploy your own account/ contract and extract privatekey inorder to get the expected result
*/

// TestInteractToEnterpriseSmartContractWithValidProviderSignatureFromAccountWithoutGas
// Will attempt to reproduce logic of provider paying gas fee.
// It should be send from address without any native token
// The balance of provider should be check prior and after the transaction is mined to
// assure the correctness of the program.
func TestInteractToEnterpriseSmartContractWithValidProviderSignatureFromAccountWithoutGas(t *testing.T) {
	const (
		senderWithoutGasPK      = "CD79C18795A866C4A7FA8D3A88494F618AB0E69B1493382D638A6483538EEA97"
		senderWithoutGasAddrStr = "0xBBD9e63B95308358AAfb20d6606701A4b6429f5e"
	)
	senderAddr := common.HexToAddress(senderWithoutGasAddrStr)
	contractAddr := common.HexToAddress(newContractAddrWithProvider)
	spk, err := crypto.HexToECDSA(senderWithoutGasPK)
	assert.NoError(t, err)

	ppk, err := crypto.HexToECDSA(providerPK)
	assert.NoError(t, err)
	signer := types.HomesteadSigner{}
	ethClient, err := ethclient.Dial(ethRPCEndpoint)
	assert.NoError(t, err)
	nonce, err := ethClient.PendingNonceAt(context.Background(), senderAddr)
	assert.NoError(t, err)
	gasPrice, err := ethClient.SuggestGasPrice(context.Background())
	assert.NoError(t, err)

	// data to interact with a function of this contract
	dataBytes := []byte("0x552410770000000000000000000000000000000000000000000000000000000000000004")
	transaction := types.NewTransaction(nonce, contractAddr, big.NewInt(0), testGasLimit, gasPrice, dataBytes)
	transaction, err = types.SignTx(transaction, signer, spk)
	assert.NoError(t, err)
	transaction, err = types.ProviderSignTx(transaction, signer, ppk)
	assert.NoError(t, err)

	assert.NoError(t, ethClient.SendTransaction(context.Background(), transaction))
}

// Interact with a payable function and sending some native token along with transaction
// Please make sure the sender does not have any funds
// expected to get revert as sender's balance is not enough for transaction amount
func TestInteractWithAmountToEnterpriseSmartContractWithValidProviderSignatureFromAccountWithoutGas(t *testing.T) {
	const (
		senderWithoutGasPK      = "CD79C18795A866C4A7FA8D3A88494F618AB0E69B1493382D638A6483538EEA97"
		senderWithoutGasAddrStr = "0xBBD9e63B95308358AAfb20d6606701A4b6429f5e"
	)
	senderAddr := common.HexToAddress(senderWithoutGasAddrStr)
	contractAddr := common.HexToAddress(newContractAddrWithProvider)
	spk, err := crypto.HexToECDSA(senderWithoutGasPK)
	assert.NoError(t, err)

	ppk, err := crypto.HexToECDSA(providerPK)
	assert.NoError(t, err)
	signer := types.HomesteadSigner{}
	ethClient, err := ethclient.Dial(ethRPCEndpoint)
	assert.NoError(t, err)
	nonce, err := ethClient.PendingNonceAt(context.Background(), senderAddr)
	assert.NoError(t, err)
	gasPrice, err := ethClient.SuggestGasPrice(context.Background())
	assert.NoError(t, err)

	// data to interact with a function of this contract
	dataBytes := []byte("0x552410770000000000000000000000000000000000000000000000000000000000000004")
	transaction := types.NewTransaction(nonce, contractAddr, big.NewInt(1000000), testGasLimit, gasPrice, dataBytes)
	transaction, err = types.SignTx(transaction, signer, spk)
	assert.NoError(t, err)
	transaction, err = types.ProviderSignTx(transaction, signer, ppk)
	assert.NoError(t, err)

	assert.NotEqual(t, nil, ethClient.SendTransaction(context.Background(), transaction))
}

// Interact with a payable function and sending some native token along with transaction
// Please make sure sender has enough balance to cover transaction amount
// expected to get passed as sender's balance is enough for transaction amount
func TestInteractWithAmountToEnterpriseSmartContractWithValidProviderSignatureFromAccountWithEnoughBalance(t *testing.T) {
	senderAddr := common.HexToAddress(senderAddrStr)
	contractAddr := common.HexToAddress(newContractAddrWithProvider)
	spk, err := crypto.HexToECDSA(senderPK)
	assert.NoError(t, err)

	ppk, err := crypto.HexToECDSA(providerPK)
	assert.NoError(t, err)
	signer := types.HomesteadSigner{}
	ethClient, err := ethclient.Dial(ethRPCEndpoint)
	assert.NoError(t, err)
	nonce, err := ethClient.PendingNonceAt(context.Background(), senderAddr)
	assert.NoError(t, err)
	gasPrice, err := ethClient.SuggestGasPrice(context.Background())
	assert.NoError(t, err)

	// data to interact with a function of this contract
	dataBytes := []byte("0x552410770000000000000000000000000000000000000000000000000000000000000004")
	transaction := types.NewTransaction(nonce, contractAddr, big.NewInt(1000000), testGasLimit, gasPrice, dataBytes)
	transaction, err = types.SignTx(transaction, signer, spk)
	assert.NoError(t, err)
	transaction, err = types.ProviderSignTx(transaction, signer, ppk)
	assert.NoError(t, err)

	assert.NoError(t, ethClient.SendTransaction(context.Background(), transaction))
}

// Interact with enterprise contract where provider has zero gas
// Please make sure sender has balance and provider has zero balance
// Expected to get failure as provider's balance is not enough for transaction fee
// Please check error message
func TestInteractEnterpriseSmartContractWithValidProviderSignatureWithoutGas(t *testing.T) {
	senderAddr := common.HexToAddress(senderAddrStr)
	contractAddr := common.HexToAddress(contractProviderWithoutGas)
	spk, err := crypto.HexToECDSA(senderPK)
	assert.NoError(t, err)

	ppk, err := crypto.HexToECDSA(providerWithoutGasPK)
	assert.NoError(t, err)
	signer := types.HomesteadSigner{}
	ethClient, err := ethclient.Dial(ethRPCEndpoint)
	assert.NoError(t, err)
	nonce, err := ethClient.PendingNonceAt(context.Background(), senderAddr)
	assert.NoError(t, err)
	gasPrice, err := ethClient.SuggestGasPrice(context.Background())
	assert.NoError(t, err)

	// data to interact with a function of this contract
	dataBytes := []byte("0x552410770000000000000000000000000000000000000000000000000000000000000004")
	transaction := types.NewTransaction(nonce, contractAddr, big.NewInt(0), testGasLimit, gasPrice, dataBytes)
	transaction, err = types.SignTx(transaction, signer, spk)
	assert.NoError(t, err)
	transaction, err = types.ProviderSignTx(transaction, signer, ppk)
	assert.NoError(t, err)

	assert.NotEqual(t, nil, ethClient.SendTransaction(context.Background(), transaction))
}
