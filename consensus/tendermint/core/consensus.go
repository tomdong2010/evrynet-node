package core

import (
	"math/big"

	"github.com/evrynet-official/evrynet-client/consensus/tendermint"
	"github.com/evrynet-official/evrynet-client/log"
)

//enterNewRound switch the core state to new round,
//it checks core state to make sure that it's legal to enterNewRound
//it set core.currentState with new params and call enterPropose
//enterNewRound is called after:
// - `timeoutNewHeight` by startTime (commitTime+timeoutCommit),
// 	or, if SkipTimeout==true, after receiving all precommits from (height,round-1)
// - `timeoutPrecommits` after any +2/3 precommits from (height,round-1)
// - +2/3 precommits for nil at (height,round-1)
// - +2/3 prevotes any or +2/3 precommits for block or any from (height, round)
// NOTE: cs.StartTime was already set for height.
func (c *core) enterNewRound(blockNumber *big.Int, round int64) {
	//This is strictly use with pointer for state update.
	var (
		state         = c.CurrentState()
		sBlockNunmber = state.BlockNumber()
		sRound        = state.Round()
		sStep         = state.Step()
	)
	if sBlockNunmber.Cmp(blockNumber) != 0 || round < sRound || (sRound == round && sStep != RoundStepNewHeight) {
		log.Debug("enterNewRound ignore: we are in a state that is ahead of the input state",
			"current_block_number", sBlockNunmber.String(), "input_block_number", blockNumber.String(),
			"current_round", sRound, "input_round", round,
			"current_step", sStep.String(), "input_step", RoundStepNewRound.String())
		return
	}

	log.Debug("enterNewRound",
		"current_block_number", sBlockNunmber.String(), "input_block_number", blockNumber.String(),
		"current_round", sRound, "input_round", round,
		"current_step", sStep.String(), "input_step", RoundStepNewRound.String())

	//if the round we enter is higher than current round, we'll have to adjust the proposer.
	if sRound < round {
		currentProposer := c.valSet.GetProposer()
		c.valSet.CalcProposer(currentProposer.Address(), round)
	}

	//Update to RoundStepNewRound
	state.UpdateRoundStep(round, RoundStepNewRound)

	//Upon NewRound, there should be no valid block yet
	//This is only valid in round 0
	if round == 0 {
		state.SetValidRoundAndBlock(-1, nil)
	}

	c.enterPropose(blockNumber, round)

}

//defaultDecideProposal is the default proposal selector
//it will prioritize validBlock, else will get its own block from tx_pool
func (c *core) defaultDecideProposal(round int64) tendermint.Proposal {
	var (
		state = c.CurrentState()
	)
	// if there is validBlock, propose it.
	if state.ValidRound() != -1 {
		log.Debug("getting the core's valid", "block", state.ValidBlock())

		return tendermint.Proposal{
			Block:    state.ValidBlock(),
			Round:    round,
			POLRound: state.ValidRound(),
		}
	}
	//TODO: remove this
	log.Debug("getting the core's block", "block", state.Block())
	//get the block node currently received from tx_pool
	return tendermint.Proposal{
		Block:    state.Block(),
		Round:    round,
		POLRound: -1,
	}
}

//enterPropose switch core state to propose step.
//it checks core state to make sure that it's legal to enterPropose
//it check if this core is proposer and send Propose
//otherwise it will set timeout and eventually call enterPrevote
//enterPropose is called after:
// enterNewRound(blockNumber,round)
func (c *core) enterPropose(blockNumber *big.Int, round int64) {
	//This is strictly use with pointer for state update.
	var (
		state         = c.CurrentState()
		sBlockNunmber = state.BlockNumber()
		sRound        = state.Round()
		sStep         = state.Step()
	)
	if sBlockNunmber.Cmp(blockNumber) != 0 || sRound > round || (sRound == round && sStep >= RoundStepPropose) {
		log.Debug("enterPropose ignore: we are in a state that is ahead of the input state",
			"current_block_number", sBlockNunmber.String(), "input_block_number", blockNumber.String(),
			"current_round", sRound, "input_round", round,
			"current_step", sStep.String(), "input_step", RoundStepPropose.String())
		return
	}

	log.Debug("enterPropose",
		"current_block_number", sBlockNunmber.String(), "input_block_number", blockNumber.String(),
		"current_round", sRound, "input_round", round,
		"current_step", sStep.String(), "input_step", RoundStepPropose.String())

	defer func() {
		// Done enterPropose:
		state.UpdateRoundStep(round, RoundStepPropose)

		// If we have the whole proposal + POL, then goto PrevoteTimeout now.
		// else, we'll enterPrevote when the rest of the proposal is received (in AddProposalBlockPart),
		if state.IsProposalComplete() {
			c.enterPrevote(blockNumber, sRound)
		}
	}()

	// if timeOutPropose, it will eventually come to enterPrevote, but the timeout might interrupt the timeOutPropose
	// to jump to a better state. Imagine that at line 91, we come to enterPrevote and a new timeout is call from there,
	// the timeout can skip this timeOutPropose.
	c.timeout.ScheduleTimeout(timeoutInfo{
		Duration:    c.config.ProposeTimeout(round),
		BlockNumber: blockNumber,
		Round:       round,
		Step:        RoundStepPropose,
	})

	if i, _ := c.valSet.GetByAddress(c.backend.Address()); i == -1 {
		log.Debug("this node is not a validator of this round", "address", c.backend.Address().String(), "block_number", blockNumber.String(), "round", round)
		return
	}
	//if we are proposer, find the latest block we're having to propose
	if c.valSet.IsProposer(c.backend.Address()) {
		log.Info("this node is proposer of this round")
		//TODO : find out if this is better than current Tendermint implementation
		//var (
		//	lockedRound = state.LockedRound()
		//	lockedBlock = state.LockedBlock()
		//)
		//// if there is a lockedBlock, set validRound and validBlock to locked one

		//if lockedRound != -1 {
		//	state.SetValidRoundAndBlock(lockedRound, lockedBlock)
		//
		//}
		proposal := c.defaultDecideProposal(round)

		c.SendPropose(&proposal)
	}
}

//defaultDoPrevote is the default process of select a block for pretoe
//it will: - prevote lockedBlock if lockedBlock !=nil
//		   - prevote for proposalReceived if valid
//		   - prevote nil otherwise
func (c *core) defaultDoPrevote(round int64) {
	var (
		state = c.CurrentState()
	)
	// If a block is locked, prevote that.
	if state.LockedRound() != -1 {
		log.Info("prevote for locked Block")
		c.SendVote(msgPrevote, state.LockedBlock(), round)
		return
	}

	// If ProposalBlock is nil, prevote nil.
	if state.ProposalReceived() == nil {
		log.Info("prevote nil")
		c.SendVote(msgPrevote, nil, round)
		return
	}

	// TODO: Validate proposal block
	//}

	// PrevoteTimeout cs.ProposalBlock
	// NOTE: the proposal signature is validated when it is received,
	log.Info("prevote for proposal block")
	c.SendVote(msgPrevote, state.ProposalReceived().Block, round)
	//core.signAddVote(types.PrevoteType, cs.ProposalBlock.Hash(), cs.ProposalBlockParts.Header())
}

// enterPrevote set core to prevote state, at which step it will:
// - decide to whether it needs to unlock if PoLCR>LLR
// - broadcastPrevote on lockedBlock if locked, or prevote for a valid proposal, else prevote nil
// - wait until it receveid 2F+1 prevotes
// - set timer if the prevotes receives dont reach majority
// enterPrevote is called
// - when `timeoutPropose` after entering Propose.
// - when proposal block and POL is ready.
func (c *core) enterPrevote(blockNumber *big.Int, round int64) {
	//TODO: write a function for this at all enter step
	//This is strictly use with pointer for state update.
	var (
		state         = c.CurrentState()
		sBlockNunmber = state.BlockNumber()
		sRound        = state.Round()
		sStep         = state.Step()
	)
	if sBlockNunmber.Cmp(blockNumber) != 0 || round < sRound || (sRound == round && sStep >= RoundStepPrevote) {
		log.Debug("enterPrevote ignore: we are in a state that is ahead of the input state",
			"current_block_number", sBlockNunmber.String(), "input_block_number", blockNumber.String(),
			"current_round", sRound, "input_round", round,
			"current_step", sStep.String(), "input_step", RoundStepPrevote.String())
		return
	}

	log.Debug("enterPrevote",
		"current_block_number", sBlockNunmber.String(),
		"current_round", sRound, "input_round", round,
		"current_step", sStep.String())

	//eventually we'll enterPrevote
	defer func() {
		state.UpdateRoundStep(round, RoundStepPrevote)
	}()
	c.defaultDoPrevote(round)
}

// Enter: if received +2/3 precommits for next round.
// Enter: any +2/3 prevotes at next round.
func (c *core) enterPrevoteWait(blockNumber *big.Int, round int64) {
	var (
		state        = c.CurrentState()
		sBlockNumber = state.BlockNumber()
		sRound       = state.Round()
		sStep        = state.Step()
	)

	if sBlockNumber.Cmp(blockNumber) != 0 || round < sRound || (sRound == round && RoundStepPrevoteWait <= sStep) {
		log.Debug("enterPrevoteWait ignore: we are in a state that is ahead of the input state",
			"current_block_number", sBlockNumber.String(), "input_block_number", blockNumber.String(),
			"current_round", sRound, "input_round", round,
			"current_step", sStep.String(), "input_step", RoundStepPrevote.String())
		return
	}
	prevotes, ok := state.GetPrevotesByRound(round)
	if !ok {
		log.Debug("enterPrevoteWait ignore: there is no prevotes", "round", round)
	}
	if !prevotes.HasTwoThirdAny() {
		log.Debug("enterPrevoteWait ignore: there is no two third votes received", "round", round)
	}
	log.Debug("enterPrevoteWait",
		"current_block_number", sBlockNumber.String(),
		"current_round", sRound, "input_round", round,
		"current_step", sStep.String())

	defer func() {
		// Done enterPrevoteWait:
		state.UpdateRoundStep(round, RoundStepPrevoteWait)
	}()

	// Wait for some more prevotes; enterPrecommit
	c.timeout.ScheduleTimeout(timeoutInfo{
		Duration:    c.config.PrevoteTimeout(round),
		BlockNumber: blockNumber,
		Round:       round,
		Step:        RoundStepPrevoteWait,
	})
}

func (c *core) enterPrecommit(blockNumber *big.Int, round int64) {
	//TODO: implement this
}

func (c *core) startRoundZero() {
	//init valset from backend
	if c.valSet == nil {
		c.valSet = c.backend.Validators(c.CurrentState().BlockNumber())

	}
	c.enterNewRound(c.CurrentState().view.BlockNumber, 0)
}
