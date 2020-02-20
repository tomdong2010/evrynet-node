package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/pkg/errors"

	"github.com/Evrynetlabs/evrynet-node/accounts/abi"
	"github.com/Evrynetlabs/evrynet-node/accounts/abi/bind"
	"github.com/Evrynetlabs/evrynet-node/accounts/abi/bind/backends"
	"github.com/Evrynetlabs/evrynet-node/cmd/utils"
	"github.com/Evrynetlabs/evrynet-node/common"
	"github.com/Evrynetlabs/evrynet-node/common/compiler"
	"github.com/Evrynetlabs/evrynet-node/core"
	"github.com/Evrynetlabs/evrynet-node/crypto"
	"github.com/Evrynetlabs/evrynet-node/log"
	"github.com/Evrynetlabs/evrynet-node/rlp"
)

const (
	StakingSCName    = "EvrynetStaking"
	SimulatedBalance = 10000000000
)

func (w *wizard) configStakingSC(genesis *core.Genesis, validators []common.Address) error {
	fmt.Println()
	fmt.Println("Specify your staking smart contract path (default = ./consensus/staking_contracts/EvrynetStaking.sol)")
	var scPath string
	for {
		if scPath = w.readDefaultString("./consensus/staking_contracts/EvrynetStaking.sol"); len(scPath) > 0 {
			break
		}
	}

	//Compile SC file to get Bytecode, ABI
	bytecodeSC, abiSC, err := compileSCFile(scPath)
	if err != nil {
		return err
	}

	//Reading params for staking SC
	var stakingSCParams []interface{}
	stakingSCParams = append(stakingSCParams, validators)
	stakingSCParams = append(stakingSCParams, w.readStakingSCParams()...)

	fmt.Println()
	fmt.Println("What is the address of staking smart contract?")
	expectedSCAddress := w.readMandatoryAddress()

	genesisAccount, err := createGenesisAccountWithStakingSC(genesis, abiSC, bytecodeSC, validators, stakingSCParams)
	if err != nil {
		return err
	}

	genesis.Config.Tendermint.StakingSCAddress = expectedSCAddress
	genesis.Alloc[*expectedSCAddress] = genesisAccount
	return nil
}

func createGenesisAccountWithStakingSC(genesis *core.Genesis, abiSC *abi.ABI, bytecodeSC string, validators []common.Address, stakingSCParams []interface{}) (core.GenesisAccount, error) {
	//Deploy contract to simulated backend.
	contractBackend, smlSCAddress, err := deployStakingSCToSimulatedBE(genesis, *abiSC, bytecodeSC, stakingSCParams)
	if err != nil {
		return core.GenesisAccount{}, err
	}

	//Then get Code & Storage of SC to assign to new address
	codeOfSC, storageOfSC := getStakingSCData(contractBackend, smlSCAddress)

	minValidatorStake, ok := stakingSCParams[len(stakingSCParams)-3].(*big.Int)
	if !ok {
		return core.GenesisAccount{}, errors.New("Failed to convert interface to *big.Int")
	}

	return core.GenesisAccount{
		Balance: new(big.Int).Mul(big.NewInt(int64(len(validators))), minValidatorStake),
		Code:    codeOfSC,
		Storage: storageOfSC,
	}, nil
}

func compileSCFile(scPath string) (string, *abi.ABI, error) {
	contracts, err := compiler.CompileSolidity("solc", scPath)
	if err != nil {
		return "", nil, errors.Errorf("Failed to compile Solidity contract: %v", err)
	}
	bytecodeSC, abiSC, err := getBytecodeAndABIOfSC(fmt.Sprintf("%s:%s", scPath, StakingSCName), contracts)
	if err != nil {
		return "", nil, errors.Errorf("Failed to get Bytecode, ABI from contract: %v", err)
	}
	if len(bytecodeSC) == 0 || abiSC == nil {
		return "", nil, errors.Errorf("Not found any EvrynetStaking contract when compile SC. Error: %+v", err)
	}
	return bytecodeSC, abiSC, nil
}

func getBytecodeAndABIOfSC(contractName string, contracts map[string]*compiler.Contract) (string, *abi.ABI, error) {
	var byteCodeSC string

	ct := contracts[contractName]
	if ct == nil {
		return "", nil, errors.Errorf("Not found any contract by key %s", contractName)
	}
	if byteCodeSC = ct.Code; len(byteCodeSC) == 0 {
		return "", nil, errors.New("Failed to get code of contract")
	}

	// Parse ABI from contract
	abiBytes, err := json.Marshal(ct.Info.AbiDefinition)
	if err != nil {
		return "", nil, errors.Errorf("Failed to parse ABI from compiler output: %v", err)
	}
	parsedABI, err := abi.JSON(strings.NewReader(string(abiBytes)))
	if err != nil {
		return "", nil, errors.Errorf("Failed to parse bytes to ABI: %v", err)
	}
	return byteCodeSC, &parsedABI, nil
}

//Simulated backend & Preparing TransactOpts which is the collection of authorization data required to create a valid transaction.
func deployStakingSCToSimulatedBE(genesis *core.Genesis, parsedABI abi.ABI, byteCodeSC string, stakingSCParams []interface{}) (*backends.SimulatedBackend, common.Address, error) {
	pKey, err := crypto.GenerateKey()
	if err != nil {
		return nil, common.Address{}, err
	}
	addr := crypto.PubkeyToAddress(pKey.PublicKey)
	contractBackend := backends.NewSimulatedBackend(core.GenesisAlloc{addr: {Balance: big.NewInt(SimulatedBalance)}}, genesis.GasLimit)

	transactOpts := bind.NewKeyedTransactor(pKey)
	smlSCAddress, _, _, err := bind.DeployContract(transactOpts, parsedABI, common.FromHex(byteCodeSC), contractBackend, stakingSCParams...)
	if err != nil {
		utils.Fatalf("Failed to deploy contract: %v", err)
	}

	contractBackend.Commit()

	return contractBackend, smlSCAddress, nil
}

func getStakingSCData(contractBackend *backends.SimulatedBackend, smlSCAddress common.Address) ([]byte, map[common.Hash]common.Hash) {
	//Get code of staking SC after deploy to simulated backend
	codeOfStakingSC, err := contractBackend.CodeAt(context.Background(), smlSCAddress, nil)
	if err != nil || len(codeOfStakingSC) == 0 {
		utils.Fatalf("Failed to get code contract: %v", err)
	}

	// Read data of contract in statedb & put to Storage of genesis account
	storage := make(map[common.Hash]common.Hash)
	if err := contractBackend.ForEachStorageAt(smlSCAddress, nil, getDataForStorage(storage)); err != nil {
		utils.Fatalf("Failed to to read all keys, values in the storage: %v", err)
	}
	return codeOfStakingSC, storage
}

func (w *wizard) readStakingSCParams() []interface{} {
	fmt.Println()
	fmt.Println("Input params to init staking SC:")
	fmt.Println("- What is the address of candidates owner?")
	_candidatesOwner := w.readMandatoryAddress()
	fmt.Println("- What is the admin address of staking SC?")
	_admin := w.readMandatoryAddress()
	fmt.Println("- How many blocks for epoch period? (default = 1024)")
	_epochPeriod := w.readDefaultBigInt(big.NewInt(1024))
	fmt.Println("- What is the max size of validators? (max number of candidates to be selected as validators for producing blocks)")
	_maxValidatorSize := w.readMandatoryBigInt()
	fmt.Println("- What is the min stake of validator? (minimum (his own) stake of each candidate to become a validator (use to slash if validator is doing malicious things))")
	_minValidatorStake := w.readMandatoryBigInt()
	fmt.Println("- What is the min cap of vote? (minimum amount of EVR tokens to vote for a candidate)")
	_minVoteCap := w.readMandatoryBigInt()
	return []interface{}{*_candidatesOwner, _epochPeriod, _maxValidatorSize, _minValidatorStake, _minVoteCap, *_admin}
}

func getDataForStorage(storage map[common.Hash]common.Hash) func(key common.Hash, val common.Hash) bool {
	return func(key, val common.Hash) bool {
		var decode []byte
		trim := bytes.TrimLeft(val.Bytes(), "\x00") // Remove 00 in bytes
		rlp.DecodeBytes(trim, &decode)
		storage[key] = common.BytesToHash(decode)
		log.Info("DecodeBytes", "value", val.String(), "decode", storage[key].String())
		return true
	}
}
