package core

import (
	"errors"
	"fmt"

	"github.com/evrynet-official/evrynet-client/common"
	"github.com/evrynet-official/evrynet-client/consensus/tendermint"
	"github.com/evrynet-official/evrynet-client/log"
	"github.com/evrynet-official/evrynet-client/rlp"
)

var (
	ErrInvalidProposalPOLRound     = errors.New("invalid proposal POL round")
	ErrInvalidProposalSignature    = errors.New("invalid proposal signature")
	ErrVoteHeightMismatch          = errors.New("vote height mismatch")
	ErrVoteInvalidValidatorAddress = errors.New("invalid validator address")
	ErrEmptyBlockProposal          = errors.New("empty block proposal")
	emptyBlockHash                 = common.Hash{}
)

// ----------------------------------------------------------------------------

// Subscribe both internal and external events
func (c *core) subscribeEvents() {
	c.events = c.backend.EventMux().Subscribe(
		// external events
		tendermint.NewBlockEvent{},
		tendermint.MessageEvent{},
		tendermint.Proposal{},
	)
}

// Unsubscribe all events
func (c *core) unsubscribeEvents() {
	c.events.Unsubscribe()
}

// handleEvents will receive messages as well as timeout and is solely responsible for state change.
func (c *core) handleEvents() {
	// Clear state
	defer func() {
		c.handlerWg.Done()
	}()

	c.handlerWg.Add(1)

	for {
		log.Info("core's waiting for new events...")
		select {
		case event, ok := <-c.events.Chan(): //backend sending something...
			if !ok {
				return
			}
			// A real event arrived, process interesting content
			switch ev := event.Data.(type) {
			case tendermint.NewBlockEvent:
				log.Info("received New Block event", "block_number", ev.Block.Number(), "block_hash", ev.Block.Hash())
				c.CurrentState().SetBlock(ev.Block)
			case tendermint.MessageEvent:
				//TODO: Handle ev.Payload, if got error then call c.backend.Gossip()
				var msg message
				if err := rlp.DecodeBytes(ev.Payload, &msg); err != nil {
					log.Error("failed to decode msg", "error", err)
				} else {
					log.Info("received Message event", "from", msg.Address, "msg_Code", msg.Code)
					if err := c.handleMsg(msg); err != nil {
						log.Error("failed to handle msg", "error", err)
					}
				}
			default:
				log.Info("Unknown event ", "event", ev)
			}
		case ti := <-c.timeout.Chan(): //something from timeout...
			c.handleTimeout(ti)
		}
	}
}

func (c *core) verifyProposal(proposal tendermint.Proposal, msg message) error {

	// Verify POLRound, which must be nil or in range [0, proposal.Round).
	if proposal.POLRound < -1 &&
		(proposal.POLRound >= 0) && proposal.POLRound >= proposal.Round {
		return ErrInvalidProposalPOLRound
	}

	// Verify signature
	signer, err := msg.GetAddressFromSignature()
	if err != nil {
		return err
	}

	// signature must come from Proposer of this round
	if c.valSet.GetProposer().Address() != signer {
		return ErrInvalidProposalSignature
	}

	if proposal.Block == nil || (proposal.Block != nil && proposal.Block.Hash().Hex() == emptyBlockHash.Hex()) {
		return ErrEmptyBlockProposal
	}
	return nil
}

func (c *core) handlePropose(msg message) error {
	log.Info("handling propose...")
	var (
		state    = c.CurrentState()
		proposal tendermint.Proposal
	)

	if err := rlp.DecodeBytes(msg.Msg, &proposal); err != nil {
		return err
	}
	log.Info("received a proposal", "from", msg.Address, "round", proposal.Round, "block_hash", proposal.Block.Hash())

	// Already have one
	// TODO: possibly catch double proposals
	if state.ProposalReceived() != nil {
		return nil
	}
	log.Info("prepare to check blockNumber...")

	// Does not apply, this is not an error but may happen due to network lattency
	if proposal.Block.Number().Cmp(state.BlockNumber()) != 0 || proposal.Round != state.Round() {
		log.Info("received proposal with different height/round",
			"current block number", state.BlockNumber().String(), "received block number", proposal.Block.Number().String(),
			"current round", state.Round(), "received round", proposal.Round)
		return nil
	}
	if err := c.verifyProposal(proposal, msg); err != nil {
		return err
	}
	log.Info("setProposal receive...", "block_hash", proposal.Block.Hash(), "block", proposal.Block.Number(), "round", proposal.Round)

	state.SetProposalReceived(&proposal)
	//// TODO: We can check if Proposal is for a different block as this is a sign of misbehavior!
	return nil
}

func (c *core) handlePrevote(msg message) error {
	var (
		vote  tendermint.Vote
		state = c.CurrentState()
	)
	if err := rlp.DecodeBytes(msg.Msg, &vote); err != nil {
		return err
	}
	if vote.BlockHash == nil {
		panic("nil block hash is not allowed. Please make sure that prevote nil send an emptyBlockHash")
	}

	if vote.BlockNumber.Cmp(state.BlockNumber()) != 0 {
		log.Warn("vote's block is different with current block, maybe some older message come after new block is reached", "current_block", state.BlockNumber(), "vote_block", vote.BlockNumber)
		return nil
	}
	log.Info("received prevote", "from", msg.Address, "round", vote.Round, "block_hash", vote.BlockHash.Hex())
	added, err := state.addPrevote(msg, &vote, c.valSet)
	if err != nil {
		return err
	}
	if !added {
		return nil
	}

	log.Info("added prevote vote into roundState", "from", msg.Address, "vote_block_number", vote.BlockNumber, "vote_round", vote.Round, "block_hash", vote.BlockHash.Hex())
	prevotes, ok := state.GetPrevotesByRound(vote.Round)
	if !ok {
		panic("expect prevotes to exist now")
	}
	//at this stage, state.PrevoteReceived[vote.Round] is guaranted to exist.
	if blockHash, ok := prevotes.TwoThirdMajority(); ok {
		log.Info("got 2/3 majority on a block", "block", blockHash.Hex())
		var (
			lockedRound = state.LockedRound()
			lockedBlock = state.LockedBlock()
		)
		//if there is a lockedRound<vote.Round <= state.Round
		//and lockedBlock != nil
		if lockedRound != -1 && lockedRound < vote.Round && vote.Round <= state.Round() && lockedBlock.Hash().Hex() != blockHash.Hex() {
			log.Info("unlocking because of POL", "lockedRound", lockedRound, "POLRound", vote.Round)
			state.Unlock()
		}

		//set valid Block if the polka is not emptyBlock
		if blockHash.Hex() != emptyBlockHash.Hex() && state.ValidRound() < vote.Round && vote.Round == state.Round() {
			if state.ProposalReceived() != nil && state.ProposalReceived().Block.Hash().Hex() == blockHash.Hex() {
				log.Info("updating validblock because of POL", "validRound", state.ValidRound(), "POLRound", vote.Round)
				state.SetValidRoundAndBlock(vote.Round, state.ProposalReceived().Block)
			} else {
				log.Info("updating proposalBlock to nil since we received a valid block we don't know about")
				state.SetProposalReceived(nil)
			}
		}
	}
	//rebroadcast
	//note that tendermint doesn't do it, but it seems like this would speed up the process of gossiping
	go func() {
		//We don't re-gossip if this is our own message
		if msg.Address == c.backend.Address() {
			return
		}
		payload, err := rlp.EncodeToBytes(&msg)
		if err != nil {
			log.Error("failed to encode msg", "error", err)
			return
		}
		if err := c.backend.Gossip(c.valSet, payload); err != nil {
			log.Error("failed to re-gossip the vote received", "error", err)
		}
	}()
	//if we receive a future roundthat come to 2/3 of prevotes on any block
	switch {
	case state.Round() < vote.Round && prevotes.HasTwoThirdAny():
		//Skip to vote.round
		c.enterNewRound(state.BlockNumber(), vote.Round)
	case state.Round() == vote.Round && RoundStepPrevote <= state.Step(): // current round
		blockHash, ok := prevotes.TwoThirdMajority()
		if ok && (state.IsProposalComplete() || blockHash.Hex() == emptyBlockHash.Hex()) {
			c.enterPrecommit(state.BlockNumber(), vote.Round)
		} else if prevotes.HasTwoThirdAny() {
			//wait till we got a majority
			c.enterPrevoteWait(state.BlockNumber(), vote.Round)
		}
	case state.ProposalReceived() != nil && 0 <= state.ProposalReceived().POLRound && state.ProposalReceived().POLRound == vote.Round:
		if state.IsProposalComplete() {
			c.enterPrevote(state.BlockNumber(), vote.Round)
		}
	}
	return nil
}

func (c *core) handlePrecommit(msg message) error {
	var (
		vote  tendermint.Vote
		state = c.CurrentState()
	)
	log.Info("handling precommit ...")
	if err := rlp.DecodeBytes(msg.Msg, &vote); err != nil {
		return err
	}
	if vote.BlockHash == nil {
		panic("nil block hash is not allowed. Please make sure that prevote nil send an emptyBlockHash")
	}
	if vote.BlockNumber.Cmp(state.BlockNumber()) != 0 {
		log.Error("vote's block is different with current block", "current_block", state.BlockNumber(), "vote_block", vote.BlockNumber)
		return nil
	}
	log.Info("received precommit", "from", msg.Address, "round", vote.Round, "block_hash", vote.BlockHash.Hex())
	added, err := state.addPrecommit(msg, &vote, c.valSet)
	if err != nil {
		return err
	}
	if !added {
		log.Info("known vote, skipping status check change and skipping re-broadcast")
		return nil
	}
	log.Info("added precommit vote into roundState", "round", vote.Round, "block_hash", vote.BlockHash.Hex(), "from", msg.Address.Hex())

	//rebroadcast
	//note that tendermint doesn't do it, but it seems like this would speed up the process of gossiping
	go func() {
		//we don't re-gossip if this is our own message
		if msg.Address == c.backend.Address() {
			return
		}
		payload, err := rlp.EncodeToBytes(&msg)
		if err != nil {
			log.Error("failed to encode msg", "error", err)
			return
		}
		if err := c.backend.Gossip(c.valSet, payload); err != nil {
			log.Error("failed to re-gossip the vote received", "error", err)
		}
	}()

	precommits, ok := state.GetPrecommitsByRound(vote.Round)
	if !ok {
		panic("expect precommits to exist now")
	}
	//at this stage, state.PrevoteReceived[vote.Round] is guaranted to exist.

	blockHash, ok := precommits.TwoThirdMajority()
	if ok {
		log.Info(" got 2/3 precommits  majority on a block", "block", blockHash)
		//this will go through the roundstep again to update core's roundState accordingly in case the vote Round is higher than core's Round
		c.enterNewRound(state.BlockNumber(), vote.Round)
		c.enterPrecommit(state.BlockNumber(), vote.Round)
		//if the precommit are not nil, enter commit
		if blockHash.Hex() != emptyBlockHash.Hex() {
			c.enterCommit(state.BlockNumber(), vote.Round)
			//TODO: if we need to skip when precommits has all votes
		} else {
			//wait for more precommit
			c.enterPrecommitWait(state.BlockNumber(), vote.Round)
		}
		return nil
	}

	//if there is no majority block
	if state.Round() <= vote.Round && precommits.HasTwoThirdAny() {
		//go through roundstep again to update round state
		c.enterNewRound(state.BlockNumber(), vote.Round)
		//wait for more precommit
		c.enterPrecommitWait(state.BlockNumber(), vote.Round)
	}
	return nil
}

func (c *core) handleMsg(msg message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch msg.Code {
	case msgPropose:
		return c.handlePropose(msg)
	case msgPrevote:
		return c.handlePrevote(msg)
	case msgPrecommit:
		return c.handlePrecommit(msg)
	default:
		return fmt.Errorf("unknown msg code %d", msg.Code)
	}
}

func (c *core) handleTimeout(ti timeoutInfo) {
	log.Info("Received timeout signal from core.timeout", "timeout", ti.Duration, "block_number", ti.BlockNumber, "round", ti.Round, "step", ti.Step)
	var (
		round       = c.CurrentState().Round()
		blockNumber = c.CurrentState().BlockNumber()
		step        = c.CurrentState().Step()
	)
	// timeouts must be for current height, round, step
	if ti.BlockNumber.Cmp(blockNumber) != 0 || ti.Round < round || (ti.Round == round && ti.Step < step) {
		log.Info("Ignoring timeout because we're ahead", "block_number", blockNumber, "round", round, "step", step)
		return
	}

	// the timeout will now cause a state transition
	c.mu.Lock()
	defer c.mu.Unlock()

	switch ti.Step {
	case RoundStepNewHeight:
		// NewRound event fired from enterNewRound.
		c.enterNewRound(ti.BlockNumber, 0)
	case RoundStepNewRound:
		c.enterPropose(ti.BlockNumber, 0)
	case RoundStepPropose:
		c.enterPrevote(ti.BlockNumber, ti.Round)
	case RoundStepPrevoteWait:
		c.enterPrecommit(ti.BlockNumber, ti.Round)
	case RoundStepPrecommitWait:
		c.enterPrecommit(ti.BlockNumber, ti.Round)
		c.enterNewRound(ti.BlockNumber, ti.Round+1)
	default:
		panic(fmt.Sprintf("Invalid timeout step: %v", ti.Step))
	}

}
