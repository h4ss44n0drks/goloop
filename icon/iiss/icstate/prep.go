package icstate

import (
	"bytes"
	"math/big"
	"sort"

	"github.com/icon-project/goloop/common/errors"
	"github.com/icon-project/goloop/common/log"
	"github.com/icon-project/goloop/icon/iiss/icutils"
	"github.com/icon-project/goloop/module"
)

type PRep struct {
	owner module.Address

	*PRepBase
	*PRepStatus
}

func (p *PRep) Owner() module.Address {
	return p.owner
}

func (p *PRep) GetNode() module.Address {
	if p.Node() != nil {
		return p.Node()
	}
	return p.owner
}

func (p *PRep) ToJSON(blockHeight int64, bondRequirement int64) map[string]interface{} {
	jso := icutils.MergeMaps(p.PRepBase.ToJSON(), p.PRepStatus.ToJSON(blockHeight, bondRequirement))
	jso["address"] = p.owner
	return jso
}

func (p *PRep) Clone() *PRep {
	return newPRep(p.owner, p.PRepBase.Clone(), p.PRepStatus.Clone())
}

func newPRep(owner module.Address, pb *PRepBase, ps *PRepStatus) *PRep {
	return &PRep{owner: owner, PRepBase: pb, PRepStatus: ps}
}

func getPRepFromState(state *State, owner module.Address) *PRep {
	pb, _ := state.GetPRepBaseByOwner(owner, false)
	if pb == nil {
		return nil
	}

	ps, _ := state.GetPRepStatusByOwner(owner, false)
	if ps == nil {
		panic(errors.Errorf("PRepStatus not found: %s", owner))
	}

	return newPRep(owner, pb, ps)
}

type PReps struct {
	totalBonded    *big.Int
	totalDelegated *big.Int // total delegated amount of all active P-Reps
	mainPReps      int
	subPReps       int
	orderedPReps   []*PRep
	prepMap        map[string]*PRep
}

func (p *PReps) load(state *State, logger log.Logger) error {
	size := state.GetActivePRepSize()

	for i := 0; i < size; i++ {
		owner := state.GetActivePRep(i)
		prep := getPRepFromState(state, owner)
		if prep == nil {
			logger.Warnf("Failed to load PRep: %s", owner)
		} else {
			p.appendPRep(owner, prep)
		}
	}

	return nil
}

func (p *PReps) appendPRep(owner module.Address, prep *PRep) {
	p.prepMap[icutils.ToKey(owner)] = prep
	if prep.PRepStatus.Status() == Active {
		p.orderedPReps = append(p.orderedPReps, prep)
		p.totalBonded.Add(p.totalBonded, prep.Bonded())
		p.totalDelegated.Add(p.totalDelegated, prep.Delegated())
		p.adjustPRepSize(prep.Grade(), true)
	}
}

func (p *PReps) adjustPRepSize(grade Grade, increment bool) {
	delta := 1
	if !increment {
		delta = -1
	}

	switch grade {
	case Main:
		p.mainPReps += delta
	case Sub:
		p.subPReps += delta
	case Candidate:
		// Nothing to do
	default:
		panic(errors.Errorf("Invalid grade: %d", grade))
	}
}

func (p *PReps) sort(br int64) {
	sort.Slice(p.orderedPReps, func(i, j int) bool {
		ret := p.orderedPReps[i].GetBondedDelegation(br).Cmp(p.orderedPReps[j].GetBondedDelegation(br))
		if ret > 0 {
			return true
		} else if ret < 0 {
			return false
		}

		ret = p.orderedPReps[i].Delegated().Cmp(p.orderedPReps[j].Delegated())
		if ret > 0 {
			return true
		} else if ret < 0 {
			return false
		}

		return bytes.Compare(p.orderedPReps[i].owner.Bytes(), p.orderedPReps[j].owner.Bytes()) > 0
	})
}

func (p *PReps) ResetAllStatus(mainPRepCount, subPRepCount int, blockHeight int64) error {
	mainPReps := 0
	subPReps := 0
	electedPRepCount := mainPRepCount + subPRepCount

	for i, prep := range p.orderedPReps {
		ls := prep.LastState()

		if i < mainPRepCount {
			prep.SetGrade(Main)
			mainPReps++
		} else if i < electedPRepCount {
			prep.SetGrade(Sub)
			subPReps++
		} else {
			prep.SetGrade(Candidate)
		}

		if prep.Grade() == Main {
			if ls == None {
				prep.SetLastState(Ready)
				prep.SetLastHeight(blockHeight)
			}
		} else {
			if ls != None {
				if err := prep.SyncBlockVoteStats(blockHeight); err != nil {
					return err
				}
			}
		}
	}

	p.mainPReps = mainPReps
	p.subPReps = subPReps
	return nil
}

func (p *PReps) GetPRepSize(grade Grade) int {
	switch grade {
	case Main:
		return p.mainPReps
	case Sub:
		return p.subPReps
	case Candidate:
		return p.Size() - p.mainPReps - p.subPReps
	default:
		panic(errors.Errorf("Invalid grade: %d", grade))
	}
}

func (p *PReps) Size() int {
	return len(p.orderedPReps)
}

func (p *PReps) TotalBonded() *big.Int {
	return p.totalBonded
}

func (p *PReps) TotalDelegated() *big.Int {
	return p.totalDelegated
}

func (p *PReps) GetPRepByIndex(i int) *PRep {
	if i < 0 || i >= len(p.orderedPReps) {
		return nil
	}
	return p.orderedPReps[i]
}

// NewOrderedPRepsWithState returns active prep list ordered by bonded delegation
func NewOrderedPRepsWithState(state *State, logger log.Logger) (*PReps, error) {
	if logger == nil {
		logger = log.WithFields(log.Fields{
			log.FieldKeyModule: "ICON",
		})
	}

	p := &PReps{
		totalDelegated: big.NewInt(0),
		totalBonded:    big.NewInt(0),
		prepMap:        make(map[string]*PRep),
	}

	if err := p.load(state, logger); err != nil {
		return nil, err
	}
	p.sort(state.GetBondRequirement())
	return p, nil
}

func newPReps(prepList []*PRep, br int64) *PReps {
	preps := newEmptyPReps()

	for _, prep := range prepList {
		preps.appendPRep(prep.Owner(), prep)
	}
	preps.sort(br)
	return preps
}

func newEmptyPReps() *PReps {
	return &PReps{
		totalDelegated: new(big.Int),
		totalBonded:    new(big.Int),
		prepMap:        make(map[string]*PRep),
	}
}
