package block

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	"github.com/icon-project/goloop/common/errors"
	"github.com/icon-project/goloop/common/log"
	"github.com/icon-project/goloop/service/txresult"

	"github.com/icon-project/goloop/common/codec"
	"github.com/icon-project/goloop/common/db"

	"github.com/icon-project/goloop/common"
	"github.com/icon-project/goloop/module"
)

const (
	configTraceBnode = false
)

var dbCodec = codec.MP

const (
	keyLastBlockHeight = "block.lastHeight"
	genesisHeight      = 0
	configCacheCap     = 10
)

type transactionLocator struct {
	BlockHeight      int64
	TransactionGroup module.TransactionGroup
	IndexInGroup     int
}

// can be disposed either automatically or by force.
type bnode struct {
	parent   *bnode
	children []*bnode
	block    module.Block
	in       *transition
	preexe   *transition

	// a block candidate has a ref.
	// a child bnode has a ref to parent.
	// manager has a ref to finalized.
	nRef int
}

func (bn *bnode) RefCount() int {
	return bn.nRef
}

func (bn *bnode) String() string {
	return fmt.Sprintf("%p{nRef:%d ID:%s}", bn, bn.nRef, common.HexPre(bn.block.ID()))
}

type chainContext struct {
	syncer  syncer
	chain   module.Chain
	sm      module.ServiceManager
	logger  log.Logger
	running bool
	trtr    RefTracer
}

type finalizationCB = func(module.Block) bool

type manager struct {
	*chainContext
	bntr  RefTracer
	nmap  map[string]*bnode
	cache *cache

	finalized       *bnode
	finalizationCBs []finalizationCB
	timestamper     module.Timestamper
}

func (m *manager) db() db.Database {
	return m.chain.Database()
}

type taskState int

const (
	executingIn taskState = iota
	validatingOut
	validatedOut
	stopped
)

type task struct {
	manager *manager
	_cb     func(module.BlockCandidate, error)
	in      *transition
	state   taskState
}

type importTask struct {
	task
	out   *transition
	block module.BlockData
	flags int
}

type proposeTask struct {
	task
	parentBlock module.Block
	votes       module.CommitVoteSet
}

func (m *manager) addNode(par *bnode, bn *bnode) {
	par.children = append(par.children, bn)
	bn.parent = par
	par.nRef++
	if configTraceBnode {
		m.bntr.TraceRef(par)
	}
	m.nmap[string(bn.block.ID())] = bn
}

func (m *manager) newCandidate(bn *bnode) *blockCandidate {
	bn.nRef++
	if configTraceBnode {
		m.bntr.TraceRef(bn)
	}
	return &blockCandidate{
		Block: bn.block,
		m:     m,
	}
}

func (m *manager) unrefNode(bn *bnode) {
	bn.nRef--
	if configTraceBnode {
		m.bntr.TraceUnref(bn)
	}
	if bn.nRef == 0 {
		par := bn.parent
		m.removeNode(bn)
		if par != nil {
			for i, c := range par.children {
				if c == bn {
					last := len(par.children) - 1
					par.children[i] = par.children[last]
					par.children[last] = nil
					par.children = par.children[:last]
				}
			}
		}
	}
}

func (m *manager) _removeNode(bn *bnode) {
	for _, c := range bn.children {
		m._removeNode(c)
	}
	bn.in.dispose()
	bn.preexe.dispose()
	bn.parent = nil
	if configTraceBnode {
		m.bntr.TraceDispose(bn)
	}
	delete(m.nmap, string(bn.block.ID()))
}

func (m *manager) removeNode(bn *bnode) {
	for _, c := range bn.children {
		m._removeNode(c)
	}
	bn.in.dispose()
	bn.preexe.dispose()
	if bn.parent != nil {
		m.unrefNode(bn.parent)
		bn.parent = nil
	}
	if configTraceBnode {
		m.bntr.TraceDispose(bn)
	}
	delete(m.nmap, string(bn.block.ID()))
}

func (m *manager) removeNodeExcept(bn *bnode, except *bnode) {
	for _, c := range bn.children {
		if c == except {
			c.parent = nil
		} else {
			m._removeNode(c)
		}
	}
	bn.in.dispose()
	bn.preexe.dispose()
	if bn.parent != nil {
		m.unrefNode(bn.parent)
		bn.parent = nil
	}
	if configTraceBnode {
		m.bntr.TraceDispose(bn)
	}
	delete(m.nmap, string(bn.block.ID()))
}

func (t *task) cb(block module.BlockCandidate, err error) {
	cb := t._cb
	t.manager.syncer.callLater(func() {
		cb(block, err)
	})
}

func (m *manager) _import(
	block module.BlockData,
	flags int,
	cb func(module.BlockCandidate, error),
) (*importTask, error) {
	bn := m.nmap[string(block.PrevID())]
	if bn == nil {
		return nil, errors.Errorf("InvalidPreviousID(%x)", block.PrevID())
	}
	var validators module.ValidatorList
	if block.Height() == 1 {
		validators = nil
	} else {
		pprev, err := m.getBlock(bn.block.PrevID())
		if err != nil {
			return nil, errors.InvalidStateError.Wrapf(err, "Cannot get prev block %x", bn.block.PrevID())
		}
		validators = pprev.NextValidators()
	}
	if err := verifyBlock(block, bn.block, validators); err != nil {
		return nil, err
	}
	it := &importTask{
		block: block,
		flags: flags,
		task: task{
			manager: m,
			_cb:     cb,
		},
	}
	it.state = executingIn
	var err error
	it.in, err = bn.preexe.patch(it.block.PatchTransactions(), block, it)
	if err != nil {
		return nil, err
	}
	return it, nil
}

func (it *importTask) stop() {
	if it.in != nil {
		it.in.dispose()
	}
	if it.out != nil {
		it.out.dispose()
	}
	it.state = stopped
}

func (it *importTask) Cancel() bool {
	it.manager.syncer.begin()
	defer it.manager.syncer.end()

	switch it.state {
	case executingIn:
		it.stop()
	case validatingOut:
		it.stop()
	default:
		it.manager.logger.Debugf("Cancel Import: Ignored\n")
		return false
	}
	it.manager.logger.Debugf("Cancel Import: OK\n")
	return true
}

func (it *importTask) onValidate(err error) {
	it.manager.syncer.callLaterInLock(func() {
		it._onValidate(err)
	})
}

func (it *importTask) _onValidate(err error) {
	if it.state == executingIn {
		if err != nil {
			it.stop()
			it.cb(nil, err)
			return
		}
	} else if it.state == validatingOut {
		if err != nil {
			it.stop()
			it.cb(nil, err)
			return
		}
		var bn *bnode
		var ok bool
		if bn, ok = it.manager.nmap[string(it.block.ID())]; !ok {
			blockV2 := it.block.(*blockV2)
			validatedBlock := *blockV2
			validatedBlock._nextValidators = it.in.mtransition().NextValidators()
			bn = &bnode{
				block:  &validatedBlock,
				in:     it.in.newTransition(nil),
				preexe: it.out.newTransition(nil),
			}
			if configTraceBnode {
				it.manager.bntr.TraceNew(bn)
			}
			pbn := it.manager.nmap[string(it.block.PrevID())]
			it.manager.addNode(pbn, bn)
		}
		it.stop()
		it.state = validatedOut
		it.cb(it.manager.newCandidate(bn), err)
	}
}

func (it *importTask) onExecute(err error) {
	it.manager.syncer.callLaterInLock(func() {
		it._onExecute(err)
	})
}

func (it *importTask) _onExecute(err error) {
	if it.state == executingIn {
		var tr *transition
		if err != nil {
			if it.flags&module.ImportByForce > 0 {
				tr, err = it.in.sync(it.block.Result(), it.block.NextValidatorsHash(), it)
				if err == nil {
					it.in.dispose()
					it.in = tr
					return
				}
			}
			it.stop()
			it.cb(nil, err)
			return
		}
		err = it.in.verifyResult(it.block)
		if err != nil {
			// verification cannot fail in forced sync case
			if it.flags&module.ImportByForce > 0 {
				tr, err = it.in.sync(it.block.Result(), it.block.NextValidatorsHash(), it)
				if err == nil {
					it.in.dispose()
					it.in = tr
					return
				}
			}
			it.stop()
			it.cb(nil, err)
			return
		}
		it.out, err = it.in.transit(it.block.NormalTransactions(), it.block, it)
		if err != nil {
			it.stop()
			it.cb(nil, err)
			return
		}
		it.state = validatingOut
		return
	}
}

func (m *manager) _propose(
	parentID []byte,
	votes module.CommitVoteSet,
	cb func(module.BlockCandidate, error),
) (*proposeTask, error) {
	bn := m.nmap[string(parentID)]
	if bn == nil {
		return nil, errors.Errorf("NoParentBlock(id=<%x>)", parentID)
	}
	var validators module.ValidatorList
	if bn.block.Height() == 0 {
		validators = nil
	} else {
		pprev, err := m.getBlock(bn.block.PrevID())
		if err != nil {
			return nil, errors.InvalidStateError.Wrapf(err, "Cannot get prev block %x", bn.block.PrevID())
		}
		validators = pprev.NextValidators()
	}
	if err := votes.Verify(bn.block, validators); err != nil {
		return nil, err
	}
	pt := &proposeTask{
		task: task{
			manager: m,
			_cb:     cb,
		},
		parentBlock: bn.block,
		votes:       votes,
	}
	pt.state = executingIn
	patches := m.sm.GetPatches(
		bn.in.mtransition(),
		newBlockInfo(bn.block.Height()+1, votes.Timestamp()),
	)
	var err error
	pt.in, err = bn.preexe.patch(patches, nil, pt)
	if err != nil {
		return nil, err
	}
	return pt, nil
}

func (pt *proposeTask) stop() {
	if pt.in != nil {
		pt.in.dispose()
	}
	pt.state = stopped
}

func (pt *proposeTask) Cancel() bool {
	pt.manager.syncer.begin()
	defer pt.manager.syncer.end()

	switch pt.state {
	case executingIn:
		pt.stop()
	default:
		pt.manager.logger.Debugf("Cancel Propose: Ignored\n")
		return false
	}
	pt.manager.logger.Debugf("Cancel Propose: OK\n")
	return true
}

func (pt *proposeTask) onValidate(err error) {
	pt.manager.syncer.callLaterInLock(func() {
		pt._onValidate(err)
	})
}

func (pt *proposeTask) _onValidate(err error) {
	if err != nil {
		pt.stop()
		pt.cb(nil, err)
		return
	}
}

func (pt *proposeTask) onExecute(err error) {
	pt.manager.syncer.callLaterInLock(func() {
		pt._onExecute(err)
	})
}

func (pt *proposeTask) _onExecute(err error) {
	if err != nil {
		pt.stop()
		pt.cb(nil, err)
		return
	}
	height := pt.parentBlock.Height() + 1
	timestamp := pt.votes.Timestamp()
	if pt.manager.timestamper != nil {
		timestamp = pt.manager.timestamper.GetBlockTimestamp(height, timestamp)
	}
	tr, err := pt.in.propose(newBlockInfo(height, timestamp), nil)
	if err != nil {
		pt.stop()
		pt.cb(nil, err)
		return
	}
	pmtr := pt.in.mtransition()
	mtr := tr.mtransition()
	block := &blockV2{
		height:             height,
		timestamp:          timestamp,
		proposer:           pt.manager.chain.Wallet().Address(),
		prevID:             pt.parentBlock.ID(),
		logsBloom:          pmtr.LogsBloom(),
		result:             pmtr.Result(),
		patchTransactions:  pmtr.PatchTransactions(),
		normalTransactions: mtr.NormalTransactions(),
		nextValidatorsHash: pmtr.NextValidators().Hash(),
		_nextValidators:    pmtr.NextValidators(),
		votes:              pt.votes,
	}
	var bn *bnode
	var ok bool
	if bn, ok = pt.manager.nmap[string(block.ID())]; !ok {
		bn = &bnode{
			block:  block,
			in:     pt.in.newTransition(nil),
			preexe: tr,
		}
		if configTraceBnode {
			pt.manager.bntr.TraceNew(bn)
		}
		pbn := pt.manager.nmap[string(block.PrevID())]
		pt.manager.addNode(pbn, bn)
	} else {
		tr.dispose()
	}
	pt.stop()
	pt.state = validatedOut
	pt.cb(pt.manager.newCandidate(bn), nil)
	return
}

// NewManager creates BlockManager.
func NewManager(chain module.Chain, timestamper module.Timestamper) (module.BlockManager, error) {
	logger := chain.Logger().WithFields(log.Fields{
		log.FieldKeyModule: "BM",
	})
	logger.Debugf("NewBlockManager\n")
	m := &manager{
		chainContext: &chainContext{
			chain:   chain,
			sm:      chain.ServiceManager(),
			logger:  logger,
			running: true,
		},
		nmap:        make(map[string]*bnode),
		cache:       newCache(configCacheCap),
		timestamper: timestamper,
	}
	m.bntr.Logger = chain.Logger().WithFields(log.Fields{
		log.FieldKeyModule: "BM|BNODE",
	})
	m.chainContext.trtr.Logger = chain.Logger().WithFields(log.Fields{
		log.FieldKeyModule: "BM|TRANS",
	})
	chainPropBucket, err := m.bucketFor(db.ChainProperty)
	if err != nil {
		return nil, err
	}

	var height int64
	err = chainPropBucket.get(raw(keyLastBlockHeight), &height)
	if errors.NotFoundError.Equals(err) || (err == nil && height == 0) {
		if _, err := m.finalizeGenesisBlock(nil, 0, chain.CommitVoteSetDecoder()(nil)); err != nil {
			return nil, err
		}
		return m, nil
	} else if err != nil {
		return nil, err
	}
	lastFinalized, err := m.getBlockByHeight(height)
	if err != nil {
		return nil, err
	}
	if nid, err := m.sm.GetNetworkID(lastFinalized.Result()); err != nil {
		return nil, err
	} else if int(nid) != m.chain.NID() {
		return nil, errors.InvalidNetworkError.Errorf(
			"InvalidNetworkID Database.NID=%#x Chain.NID=%#x", nid, m.chain.NID())
	}
	mtr, _ := m.sm.CreateInitialTransition(lastFinalized.Result(), lastFinalized.NextValidators())
	if mtr == nil {
		return nil, err
	}
	tr := newInitialTransition(mtr, m.chainContext)
	bn := &bnode{
		block: lastFinalized,
		in:    tr,
	}
	bn.preexe, err = tr.transit(lastFinalized.NormalTransactions(), lastFinalized, nil)
	if err != nil {
		return nil, err
	}
	m.finalized = bn
	bn.nRef++
	if configTraceBnode {
		m.bntr.TraceNew(bn)
	}
	m.nmap[string(lastFinalized.ID())] = bn
	return m, nil
}

func (m *manager) Term() {
	m.syncer.begin()
	defer m.syncer.end()

	m.logger.Debugf("Term block manager\n")

	m.removeNode(m.finalized)
	m.finalized = nil
	m.running = false
}

func (m *manager) GetBlock(id []byte) (module.Block, error) {
	m.syncer.begin()
	defer m.syncer.end()

	return m.getBlock(id)
}

func (m *manager) getBlock(id []byte) (module.Block, error) {
	blk := m.cache.Get(id)
	if blk != nil {
		return blk, nil
	}
	return m.doGetBlock(id)
}

func (m *manager) doGetBlock(id []byte) (module.Block, error) {
	hb, err := m.bucketFor(db.BytesByHash)
	if err != nil {
		return nil, err
	}
	headerBytes, err := hb.getBytes(raw(id))
	if err != nil {
		return nil, err
	}
	if headerBytes == nil {
		return nil, errors.InvalidStateError.Errorf("nil header")
	}
	blk, err := m.newBlockFromHeaderReader(bytes.NewReader(headerBytes))
	if blk != nil {
		m.cache.Put(blk)
	}
	return blk, err
}

func (m *manager) Import(
	r io.Reader,
	flags int,
	cb func(module.BlockCandidate, error),
) (module.Canceler, error) {
	m.syncer.begin()
	defer m.syncer.end()

	m.logger.Debugf("Import(%x)\n", r)

	block, err := m.newBlockDataFromReader(r)
	if err != nil {
		return nil, err
	}
	it, err := m._import(block, flags, cb)
	if err != nil {
		return nil, err
	}
	return it, nil;
}

func (m *manager) ImportBlock(
	block module.BlockData,
	flags int,
	cb func(module.BlockCandidate, error),
) (module.Canceler, error) {
	m.syncer.begin()
	defer m.syncer.end()

	m.logger.Debugf("ImportBlock(%x)\n", block.ID())

	it, err := m._import(block, flags, cb)
	if err != nil {
		return nil, err
	}
	return it, nil
}

type channelingCB struct {
	ch chan<- error
}

func (cb *channelingCB) onValidate(err error) {
	cb.ch <- err
}

func (cb *channelingCB) onExecute(err error) {
	cb.ch <- err
}

func (m *manager) finalizeGenesisBlock(
	proposer module.Address,
	timestamp int64,
	votes module.CommitVoteSet,
) (block module.Block, err error) {
	m.logger.Debugf("FinalizeGenesisBlock()\n")
	if m.finalized != nil {
		return nil, errors.InvalidStateError.New("InvalidState")
	}
	mtr, err := m.sm.CreateInitialTransition(nil, nil)
	if err != nil {
		return nil, err
	}
	in := newInitialTransition(mtr, m.chainContext)
	ch := make(chan error)
	gtxbs := m.chain.Genesis()
	gtx, err := m.sm.GenesisTransactionFromBytes(gtxbs, module.BlockVersion2)
	if err != nil {
		return nil, err
	}
	if !gtx.ValidateNetwork(m.chain.NID()) {
		return nil, errors.InvalidNetworkError.Errorf(
			"Invalid Network ID config=%#x genesis=%s", m.chain.NID(), gtxbs)
	}
	gtxl := m.sm.TransactionListFromSlice([]module.Transaction{gtx}, module.BlockVersion2)
	m.syncer.begin()
	gtr, err := in.transit(gtxl, newBlockInfo(0, timestamp), &channelingCB{ch: ch})
	if err != nil {
		m.syncer.end()
		return nil, err
	}
	m.syncer.end()

	// wait for genesis transition execution
	// TODO rollback
	if err = <-ch; err != nil {
		return nil, err
	}
	if err = <-ch; err != nil {
		return nil, err
	}

	bn := &bnode{}
	bn.in = in
	bn.preexe = gtr
	bn.block = &blockV2{
		height:             genesisHeight,
		timestamp:          timestamp,
		proposer:           proposer,
		prevID:             nil,
		logsBloom:          mtr.LogsBloom(),
		result:             mtr.Result(),
		patchTransactions:  gtr.mtransition().PatchTransactions(),
		normalTransactions: gtr.mtransition().NormalTransactions(),
		nextValidatorsHash: gtr.mtransition().NextValidators().Hash(),
		_nextValidators:    gtr.mtransition().NextValidators(),
		votes:              votes,
	}
	if configTraceBnode {
		m.bntr.TraceNew(bn)
	}
	m.nmap[string(bn.block.ID())] = bn
	err = m.finalize(bn)
	if err != nil {
		return nil, err
	}
	err = m.sm.Finalize(gtr.mtransition(), module.FinalizeNormalTransaction|module.FinalizePatchTransaction|module.FinalizeResult)
	if err != nil {
		return nil, err
	}
	return bn.block, nil
}

func (m *manager) Propose(
	parentID []byte,
	votes module.CommitVoteSet,
	cb func(module.BlockCandidate, error),
) (canceler module.Canceler, err error) {
	m.syncer.begin()
	defer m.syncer.end()

	m.logger.Debugf("Propose(<%x>, %v)\n", parentID, votes)

	pt, err := m._propose(parentID, votes, cb)
	if err != nil {
		return nil, err
	}
	return pt, nil
}

func (m *manager) Commit(block module.BlockCandidate) error {
	return nil
}

func (m *manager) bucketFor(id db.BucketID) (*bucket, error) {
	b, err := m.db().GetBucket(id)
	if err != nil {
		return nil, err
	}
	return &bucket{
		dbBucket: b,
		codec:    dbCodec,
	}, nil
}

func (m *manager) Finalize(block module.BlockCandidate) error {
	m.syncer.begin()
	defer m.syncer.end()

	bn := m.nmap[string(block.ID())]
	if bn == nil || bn.parent != m.finalized {
		return errors.Errorf("InvalidStatusForBlock(id=<%x>", block.ID())
	}
	return m.finalize(bn)
}

func (m *manager) finalize(bn *bnode) error {
	// TODO notify import/propose error due to finalization
	// TODO update nmap
	block := bn.block

	if m.finalized != nil {
		m.removeNodeExcept(m.finalized, bn)
		err := m.sm.Finalize(
			bn.in.mtransition(),
			module.FinalizePatchTransaction|module.FinalizeResult,
		)
		if err != nil {
			return err
		}
	}
	err := m.sm.Finalize(bn.preexe.mtransition(), module.FinalizeNormalTransaction)
	if err != nil {
		return err
	}

	m.finalized = bn
	bn.nRef++
	if configTraceBnode {
		m.bntr.TraceRef(bn)
	}

	if blockV2, ok := block.(*blockV2); ok {
		hb, err := m.bucketFor(db.BytesByHash)
		if err != nil {
			return err
		}
		if err = hb.put(blockV2._headerFormat()); err != nil {
			return err
		}
		if err = hb.set(raw(block.Votes().Hash()), raw(block.Votes().Bytes())); err != nil {
			return err
		}
		lb, err := m.bucketFor(db.TransactionLocatorByHash)
		if err != nil {
			return err
		}
		for it := block.PatchTransactions().Iterator(); it.Has(); it.Next() {
			tr, i, err := it.Get()
			if err != nil {
				return err
			}
			trLoc := transactionLocator{
				BlockHeight:      block.Height(),
				TransactionGroup: module.TransactionGroupPatch,
				IndexInGroup:     i,
			}
			if err = lb.set(raw(tr.ID()), trLoc); err != nil {
				return err
			}
		}
		for it := block.NormalTransactions().Iterator(); it.Has(); it.Next() {
			tr, i, err := it.Get()
			if err != nil {
				return err
			}
			trLoc := transactionLocator{
				BlockHeight:      block.Height(),
				TransactionGroup: module.TransactionGroupNormal,
				IndexInGroup:     i,
			}
			if err = lb.set(raw(tr.ID()), trLoc); err != nil {
				return err
			}
		}
		b, err := m.bucketFor(db.BlockHeaderHashByHeight)
		if err != nil {
			return err
		}
		if err = b.set(block.Height(), raw(block.ID())); err != nil {
			return err
		}
		chainProp, err := m.bucketFor(db.ChainProperty)
		if err != nil {
			return err
		}
		if err = chainProp.set(raw(keyLastBlockHeight), block.Height()); err != nil {
			return err
		}
	}
	m.logger.Debugf("Finalize(%x)\n", block.ID())
	for i := 0; i < len(m.finalizationCBs); {
		cb := m.finalizationCBs[i]
		if cb(block) {
			last := len(m.finalizationCBs) - 1
			m.finalizationCBs[i] = m.finalizationCBs[last]
			m.finalizationCBs[last] = nil
			m.finalizationCBs = m.finalizationCBs[:last]
			continue
		}
		i++
	}
	return nil
}

func (m *manager) commitVoteSetFromHash(hash []byte) module.CommitVoteSet {
	hb, err := m.bucketFor(db.BytesByHash)
	if err != nil {
		return nil
	}
	bs, err := hb.getBytes(raw(hash))
	if err != nil {
		return nil
	}
	dec := m.chain.CommitVoteSetDecoder()
	return dec(bs)
}

func newAddress(bs []byte) module.Address {
	if bs != nil {
		return common.NewAddress(bs)
	}
	return nil
}

func (m *manager) newBlockFromHeaderReader(r io.Reader) (module.Block, error) {
	var header blockV2HeaderFormat
	err := v2Codec.Unmarshal(r, &header)
	if err != nil {
		return nil, err
	}
	patches := m.sm.TransactionListFromHash(header.PatchTransactionsHash)
	if patches == nil {
		return nil, errors.Errorf("TranscationListFromHash(%x) failed", header.PatchTransactionsHash)
	}
	normalTxs := m.sm.TransactionListFromHash(header.NormalTransactionsHash)
	if normalTxs == nil {
		return nil, errors.Errorf("TransactionListFromHash(%x) failed", header.NormalTransactionsHash)
	}
	nextValidators := m.sm.ValidatorListFromHash(header.NextValidatorsHash)
	if nextValidators == nil {
		return nil, errors.Errorf("ValidatorListFromHas(%x)", header.NextValidatorsHash)
	}
	votes := m.commitVoteSetFromHash(header.VotesHash)
	if votes == nil {
		return nil, errors.Errorf("commitVoteSetFromHash(%x) failed", header.VotesHash)
	}
	return &blockV2{
		height:             header.Height,
		timestamp:          header.Timestamp,
		proposer:           newAddress(header.Proposer),
		prevID:             header.PrevID,
		logsBloom:          txresult.NewLogsBloomFromCompressed(header.LogsBloom),
		result:             header.Result,
		patchTransactions:  patches,
		normalTransactions: normalTxs,
		nextValidatorsHash: nextValidators.Hash(),
		_nextValidators:    nextValidators,
		votes:              votes,
	}, nil
}

func (m *manager) newTransactionListFromBSS(
	bss [][]byte,
	version int,
) (module.TransactionList, error) {
	ts := make([]module.Transaction, len(bss))
	for i, bs := range bss {
		if tx, err := m.sm.TransactionFromBytes(bs, version); err != nil {
			return nil, err
		} else {
			ts[i] = tx
		}
	}
	return m.sm.TransactionListFromSlice(ts, version), nil
}

func (m *manager) NewBlockDataFromReader(r io.Reader) (module.BlockData, error) {
	m.syncer.begin()
	defer m.syncer.end()

	return m.newBlockDataFromReader(r)
}

func (m *manager) newBlockDataFromReader(r io.Reader) (module.BlockData, error) {
	r = bufio.NewReader(r)
	var blockFormat blockV2Format
	err := v2Codec.Unmarshal(r, &blockFormat.blockV2HeaderFormat)
	if err != nil {
		return nil, err
	}
	err = v2Codec.Unmarshal(r, &blockFormat.blockV2BodyFormat)
	if err != nil {
		return nil, err
	}
	patches, err := m.newTransactionListFromBSS(
		blockFormat.PatchTransactions,
		module.BlockVersion2,
	)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(patches.Hash(), blockFormat.PatchTransactionsHash) {
		return nil, errors.New("bad patch transactions hash")
	}
	normalTxs, err := m.newTransactionListFromBSS(
		blockFormat.NormalTransactions,
		module.BlockVersion2,
	)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(normalTxs.Hash(), blockFormat.NormalTransactionsHash) {
		return nil, errors.New("bad normal transactions hash")
	}
	// nextValidators may be nil
	nextValidators := m.sm.ValidatorListFromHash(blockFormat.NextValidatorsHash)
	votes := m.chain.CommitVoteSetDecoder()(blockFormat.Votes)
	if !bytes.Equal(votes.Hash(), blockFormat.VotesHash) {
		return nil, errors.New("bad vote list hash")
	}
	return &blockV2{
		height:             blockFormat.Height,
		timestamp:          blockFormat.Timestamp,
		proposer:           newAddress(blockFormat.Proposer),
		prevID:             blockFormat.PrevID,
		logsBloom:          txresult.NewLogsBloomFromCompressed(blockFormat.LogsBloom),
		result:             blockFormat.Result,
		patchTransactions:  patches,
		normalTransactions: normalTxs,
		nextValidatorsHash: blockFormat.NextValidatorsHash,
		_nextValidators:    nextValidators,
		votes:              votes,
	}, nil
}

type transactionInfo struct {
	_sm      module.ServiceManager
	_txID    []byte
	_txBlock module.Block
	_index   int
	_group   module.TransactionGroup
	_mtr     module.Transaction
	_rBlock  module.Block
}

func (txInfo *transactionInfo) Block() module.Block {
	return txInfo._txBlock
}

func (txInfo *transactionInfo) Index() int {
	return txInfo._index
}

func (txInfo *transactionInfo) Group() module.TransactionGroup {
	return txInfo._group
}

func (txInfo *transactionInfo) Transaction() module.Transaction {
	return txInfo._mtr
}

func (txInfo *transactionInfo) GetReceipt() (module.Receipt, error) {
	rblock := txInfo._rBlock
	if rblock != nil {
		rl, err := txInfo._sm.ReceiptListFromResult(rblock.Result(), txInfo._group)
		if err != nil {
			return nil, err
		}
		if rct, err := rl.Get(int(txInfo._index)); err == nil {
			return rct, nil
		} else {
			return nil, err
		}
	}
	return nil, ErrResultNotFinalized
}

func (m *manager) GetTransactionInfo(id []byte) (module.TransactionInfo, error) {
	m.syncer.begin()
	defer m.syncer.end()

	return m.getTransactionInfo(id)
}

func (m *manager) getTransactionInfo(id []byte) (module.TransactionInfo, error) {
	tlb, err := m.bucketFor(db.TransactionLocatorByHash)
	if err != nil {
		return nil, err
	}
	var loc transactionLocator
	err = tlb.get(raw(id), &loc)
	if err != nil {
		return nil, errors.NotFoundError.New("Not found")
	}
	block, err := m.getBlockByHeight(loc.BlockHeight)
	if err != nil {
		return nil, errors.InvalidStateError.Wrapf(err, "block h=%d not found", loc.BlockHeight)
	}

	var txs module.TransactionList
	if loc.TransactionGroup == module.TransactionGroupNormal {
		txs = block.NormalTransactions()
	} else {
		txs = block.PatchTransactions()
	}
	mtr, err := txs.Get(loc.IndexInGroup)
	if err != nil {
		return nil, errors.InvalidStateError.Wrapf(err,
			"transaction group=%d i=%d not in block h=%d",
			loc.TransactionGroup, loc.IndexInGroup, loc.BlockHeight)
	}
	var rblock module.Block
	if loc.TransactionGroup == module.TransactionGroupNormal {
		if m.finalized.block.Height() < loc.BlockHeight+1 {
			rblock = nil
		} else {
			rblock, err = m.getBlockByHeight(loc.BlockHeight + 1)
			if err != nil {
				return nil, err
			}
		}
	} else {
		rblock = block
	}
	return &transactionInfo{
		_sm:      m.sm,
		_txID:    id,
		_txBlock: block,
		_index:   loc.IndexInGroup,
		_group:   loc.TransactionGroup,
		_mtr:     mtr,
		_rBlock:  rblock,
	}, nil
}

func (m *manager) GetBlockByHeight(height int64) (module.Block, error) {
	m.syncer.begin()
	defer m.syncer.end()

	return m.getBlockByHeight(height)
}

func (m *manager) getBlockByHeight(height int64) (module.Block, error) {
	blk := m.cache.GetByHeight(height)
	if blk != nil {
		return blk, nil
	}
	return m.doGetBlockByHeight(height)
}

func (m *manager) doGetBlockByHeight(height int64) (module.Block, error) {
	headerHashByHeight, err := m.bucketFor(db.BlockHeaderHashByHeight)
	if err != nil {
		return nil, err
	}
	hash, err := headerHashByHeight.getBytes(height)
	if err != nil {
		return nil, err
	}
	blk, err := m.doGetBlock(hash)
	if errors.NotFoundError.Equals(err) {
		return blk, errors.InvalidStateError.Wrapf(err, "block h=%d by hash=%x not found", height, hash)
	}
	return blk, err
}

func (m *manager) GetLastBlock() (module.Block, error) {
	m.syncer.begin()
	defer m.syncer.end()

	return m.finalized.block, nil
}

func (m *manager) WaitForBlock(height int64) (<-chan module.Block, error) {
	m.syncer.begin()
	defer m.syncer.end()

	bch := make(chan module.Block, 1)

	blk, err := m.getBlockByHeight(height)
	if err == nil {
		bch <- blk
		return bch, nil
	} else if !errors.NotFoundError.Equals(err) {
		return nil, err
	}

	m.finalizationCBs = append(m.finalizationCBs, func(blk module.Block) bool {
		if blk.Height() == height {
			bch <- blk
			return true
		}
		return false
	})
	return bch, nil
}

func (m *manager) WaitForTransaction(parentID []byte, cb func()) bool {
	m.syncer.begin()
	defer m.syncer.end()

	bn := m.nmap[string(parentID)]
	if bn == nil {
		return false
	}
	return m.sm.WaitForTransaction(bn.in.mtransition(), bn.block, cb)
}

func (m *manager) DupBlockCandidate(bc *blockCandidate) *blockCandidate {
	m.syncer.begin()
	defer m.syncer.end()

	bn := m.nmap[string(bc.ID())]
	if bn != nil {
		bn.nRef++
		if configTraceBnode {
			m.bntr.TraceRef(bn)
		}
	}
	res := *bc
	return &res
}

func (m *manager) DisposeBlockCandidate(bc *blockCandidate) {
	m.syncer.begin()
	defer m.syncer.end()

	bn := m.nmap[string(bc.ID())]
	if bn == nil {
		return
	}
	m.unrefNode(bn)
}
