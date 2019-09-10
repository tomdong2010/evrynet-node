package core

import (
	"io"
	"math/big"
	"sync"

	"github.com/evrynet-official/evrynet-client/core/types"
	"github.com/evrynet-official/evrynet-client/crypto"

	"github.com/evrynet-official/evrynet-client/common"
	"github.com/evrynet-official/evrynet-client/consensus/tendermint"
	"github.com/evrynet-official/evrynet-client/rlp"
)

//Engine abstract the core's functionalities
//Note that backend and other packages doesn't care about core's internal logic.
//It only requires core to start receiving/handling messages
//The sending of events/message from core to backend will be done by calling accessing Backend.EventMux()
type Engine interface {
	Start() error
	Stop() error
	//SetBlockForProposal define a method to allow Injecting a Block for testing purpose
	SetBlockForProposal(block *types.Block)
}

// TODO: More msg codes here if needed
const (
	msgPropose uint64 = iota
	msgPrevote
	msgPrecommit
)

type message struct {
	Code      uint64
	Msg       []byte
	Address   common.Address
	Signature []byte
	//TODO: Is CommitedSeal needed in message of Tendermint?
	CommittedSeal []byte
}

// EncodeRLP serializes m into the Ethereum RLP format.
func (m *message) EncodeRLP(w io.Writer) error {
	return rlp.Encode(w, []interface{}{m.Code, m.Msg, m.Address, m.Signature, m.CommittedSeal})
}

// DecodeRLP implements rlp.Decoder, and load the consensus fields from a RLP stream.
func (m *message) DecodeRLP(s *rlp.Stream) error {
	var msg struct {
		Code          uint64
		Msg           []byte
		Address       common.Address
		Signature     []byte
		CommittedSeal []byte
	}

	if err := s.Decode(&msg); err != nil {
		return err
	}
	m.Code, m.Msg, m.Address, m.Signature, m.CommittedSeal = msg.Code, msg.Msg, msg.Address, msg.Signature, msg.CommittedSeal
	return nil
}

func (m *message) PayLoadWithoutSignature() ([]byte, error) {
	return rlp.EncodeToBytes(&message{
		Code:          m.Code,
		Address:       m.Address,
		Msg:           m.Msg,
		Signature:     []byte{},
		CommittedSeal: m.CommittedSeal,
	})
}

// GetAddressFromSignature gets the signer address from the signature
func (m *message) GetAddressFromSignature() (common.Address, error) {
	payLoad, err := m.PayLoadWithoutSignature()
	if err != nil {
		return common.Address{}, err
	}
	// 2. Recover public key
	pubkey, err := crypto.SigToPub(payLoad, m.Signature)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pubkey), nil
}

type messageSet struct {
	view       *tendermint.View
	valSet     tendermint.ValidatorSet
	messagesMu *sync.Mutex
	messages   map[common.Address]*message
}

// Construct a new message set to accumulate messages for given height/view number.
func newMessageSet(valSet tendermint.ValidatorSet) *messageSet {
	return &messageSet{
		view: &tendermint.View{
			Round:       0,
			BlockNumber: new(big.Int),
		},
		messagesMu: new(sync.Mutex),
		messages:   make(map[common.Address]*message),
		valSet:     valSet,
	}
}