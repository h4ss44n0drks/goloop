package iiss

import (
	"math"
	"math/big"

	"github.com/icon-project/goloop/common"
	"github.com/icon-project/goloop/common/errors"
	"github.com/icon-project/goloop/common/log"
	"github.com/icon-project/goloop/icon/icmodule"
	"github.com/icon-project/goloop/icon/iiss/icstate"
	"github.com/icon-project/goloop/icon/iiss/icutils"
)

// PRepManager manages PRepBase, PRepStatus and ActivePRep objects
type PRepManager struct {
	logger log.Logger
	state  *icstate.State
}

func (pm *PRepManager) ToJSON(totalStake *big.Int) map[string]interface{} {
	preps, _ := icstate.NewOrderedPRepsWithState(pm.state, pm.logger)
	if preps == nil {
		return nil
	}

	jso := make(map[string]interface{})
	jso["totalStake"] = totalStake
	jso["totalBonded"] = preps.TotalBonded()
	jso["totalDelegated"] = preps.TotalDelegated()
	jso["preps"] = preps.Size()
	return jso
}

func (pm *PRepManager) GetPRepsInJSON(blockHeight int64, start, end int) (map[string]interface{}, error) {
	preps, err := icstate.NewOrderedPRepsWithState(pm.state, pm.logger)
	if err != nil {
		return nil, err
	}

	if start < 0 {
		return nil, errors.IllegalArgumentError.Errorf("start(%d) < 0", start)
	}
	if end < 0 {
		return nil, errors.IllegalArgumentError.Errorf("end(%d) < 0", end)
	}

	size := preps.Size()
	if start > end {
		return nil, errors.IllegalArgumentError.Errorf("start(%d) > end(%d)", start, end)
	}
	if start > size {
		return nil, errors.IllegalArgumentError.Errorf("start(%d) > # of preps(%d)", start, size)
	}
	if start == 0 {
		start = 1
	}
	if end == 0 || end > size {
		end = size
	}

	jso := make(map[string]interface{})
	prepList := make([]interface{}, 0, end)
	br := pm.state.GetBondRequirement()

	for i := start - 1; i < end; i++ {
		prep := preps.GetPRepByIndex(i)
		prepList = append(prepList, prep.ToJSON(blockHeight, br))
	}

	jso["startRanking"] = start
	jso["preps"] = prepList
	jso["totalDelegated"] = preps.TotalDelegated()
	return jso, nil
}

func (pm *PRepManager) ChangeDelegation(od, nd icstate.Delegations) (map[string]*big.Int, error) {
	delta := make(map[string]*big.Int)

	for _, d := range od {
		key := icutils.ToKey(d.To())
		delta[key] = new(big.Int).Neg(d.Amount())
	}
	for _, d := range nd {
		key := icutils.ToKey(d.To())
		if delta[key] == nil {
			delta[key] = new(big.Int)
		}
		delta[key].Add(delta[key], d.Amount())
	}

	delegatedToInactiveNode := big.NewInt(0)
	for key, value := range delta {
		owner, err := common.NewAddress([]byte(key))
		if err != nil {
			return nil, err
		}
		if value.Sign() != 0 {
			ps, _ := pm.state.GetPRepStatusByOwner(owner, true)
			ps.SetDelegated(new(big.Int).Add(ps.Delegated(), value))
			if !ps.IsActive() {
				delegatedToInactiveNode.Add(delegatedToInactiveNode, value)
			}
		}
	}

	oldTotalDelegation := pm.state.GetTotalDelegation()
	totalDelegation := new(big.Int).Set(oldTotalDelegation)
	totalDelegation.Add(totalDelegation, nd.GetDelegationAmount())
	totalDelegation.Sub(totalDelegation, od.GetDelegationAmount())
	//// Ignore the delegated amount to Inactive P-Rep
	totalDelegation.Sub(totalDelegation, delegatedToInactiveNode)

	if totalDelegation.Cmp(oldTotalDelegation) != 0 {
		if err := pm.state.SetTotalDelegation(totalDelegation); err != nil {
			return nil, err
		}
	}
	return delta, nil
}

func (pm *PRepManager) ChangeBond(oBonds, nBonds icstate.Bonds) (map[string]*big.Int, error) {
	delta := make(map[string]*big.Int)

	for _, bond := range oBonds {
		key := icutils.ToKey(bond.To())
		delta[key] = new(big.Int).Neg(bond.Amount())
	}
	for _, bond := range nBonds {
		key := icutils.ToKey(bond.To())
		if delta[key] == nil {
			delta[key] = new(big.Int)
		}
		delta[key].Add(delta[key], bond.Amount())
	}

	bondedToInactiveNode := big.NewInt(0)
	for key, value := range delta {
		owner, err := common.NewAddress([]byte(key))
		if err != nil {
			return nil, err
		}

		if value.Sign() != 0 {
			ps, _ := pm.state.GetPRepStatusByOwner(owner, false)
			if ps == nil {
				return nil, errors.Errorf("Failed to set bonded value to PRepStatus")
			}

			if ps.IsActive() {
				ps.SetBonded(new(big.Int).Add(ps.Bonded(), value))
			} else {
				// this code is not reachable, because there is no case of bonding to not-registered PRep
				bondedToInactiveNode.Add(bondedToInactiveNode, value)
			}
		}
	}

	oldTotalBond := pm.state.GetTotalBond()
	totalBond := new(big.Int).Set(oldTotalBond)
	totalBond.Add(totalBond, nBonds.GetBondAmount())
	totalBond.Sub(totalBond, oBonds.GetBondAmount())
	// Ignore the bonded amount to inactive P-Rep
	totalBond.Sub(totalBond, bondedToInactiveNode)

	if totalBond.Cmp(oldTotalBond) != 0 {
		if err := pm.state.SetTotalBond(totalBond); err != nil {
			return nil, err
		}
	}
	return delta, nil
}

func (pm *PRepManager) GetPRepStatsInJSON(blockHeight int64) (map[string]interface{}, error) {
	pss, err := pm.state.GetPRepStatuses()
	if err != nil {
		return nil, err
	}

	size := len(pss)
	jso := make(map[string]interface{})
	psList := make([]interface{}, size, size)

	for i := 0; i < size; i++ {
		ps := pss[i]
		psList[i] = ps.GetStatsInJSON(blockHeight)
	}

	jso["blockHeight"] = blockHeight
	jso["preps"] = psList
	return jso, nil
}

func (pm *PRepManager) CalculateIRep(preps *icstate.PReps, revision int) *big.Int {
	irep := new(big.Int)
	if revision < icmodule.RevisionDecentralize ||
		revision >= icmodule.RevisionICON2 {
		return irep
	}
	if revision >= icmodule.Revision9 {
		// set IRep via network proposal
		return nil
	}

	mainPRepCount := preps.GetPRepSize(icstate.Main)
	totalDelegated := new(big.Int)
	totalWeightedIrep := new(big.Int)
	value := new(big.Int)

	for i := 0; i < mainPRepCount; i++ {
		prep := preps.GetPRepByIndex(i)
		totalWeightedIrep.Add(totalWeightedIrep, value.Mul(prep.IRep(), prep.Delegated()))
		totalDelegated.Add(totalDelegated, prep.Delegated())
	}

	if totalDelegated.Sign() == 0 {
		return irep
	}

	irep.Div(totalWeightedIrep, totalDelegated)
	if irep.Cmp(icstate.BigIntMinIRep) == -1 {
		irep.Set(icstate.BigIntMinIRep)
	}
	return irep
}

func (pm *PRepManager) CalculateRRep(totalSupply *big.Int, revision int, totalDelegation *big.Int) *big.Int {
	if revision < icmodule.RevisionIISS || revision >= icmodule.RevisionICON2 {
		// rrep is disabled
		return new(big.Int)
	}
	return calculateRRep(totalSupply, totalDelegation)
}

const (
	rrepMin        = 200   // 2%
	rrepMax        = 1_200 // 12%
	rrepPoint      = 7_000 // 70%
	rrepMultiplier = 10_000
)

func calculateRRep(totalSupply, totalDelegated *big.Int) *big.Int {
	ts := new(big.Float).SetInt(totalSupply)
	td := new(big.Float).SetInt(totalDelegated)
	delegatePercentage := new(big.Float).Quo(td, ts)
	delegatePercentage.Mul(delegatePercentage, new(big.Float).SetInt64(rrepMultiplier))
	dp, _ := delegatePercentage.Float64()
	if dp >= rrepPoint {
		return new(big.Int).SetInt64(rrepMin)
	}

	firstOperand := (rrepMax - rrepMin) / math.Pow(rrepPoint, 2)
	secondOperand := math.Pow(dp-rrepPoint, 2)
	return new(big.Int).SetInt64(int64(firstOperand*secondOperand + rrepMin))
}

func newPRepManager(state *icstate.State, logger log.Logger) *PRepManager {
	if logger == nil {
		logger = icutils.NewIconLogger(nil)
	}
	return &PRepManager{
		logger: logger,
		state:  state,
	}
}
