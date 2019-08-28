package sync

import (
	"github.com/icon-project/goloop/common/db"
	"github.com/icon-project/goloop/common/log"
	"github.com/icon-project/goloop/module"
)

type server struct {
	database    db.Database
	ph          module.ProtocolHandler
	log         log.Logger
	merkleTrie  db.Bucket
	bytesByHash db.Bucket
}

func (s *server) onReceive(pi module.ProtocolInfo, b []byte, p *peer) {
	switch pi {
	case protoHasNode:
		go s.hasNode(b, p)
	case protoRequestNodeData:
		go s.requestNode(b, p)
	default:
		s.log.Infof("Invalid pi(%v)\n", pi)
	}
}

func (s *server) hasNode(msg []byte, p *peer) {
	hr := new(hasNode)
	if _, err := c.UnmarshalFromBytes(msg, &hr); err != nil {
		s.log.Debugf("Failed to unmarshal data (%#x)\n", msg)
		return
	}

	status := NoError
	for _, hash := range [][]byte{hr.StateHash, hr.PatchHash, hr.NormalHash} {
		if len(hash) == 0 {
			continue
		}
		// TODO check cache
		if v, err := s.merkleTrie.Get(hash); err != nil || v == nil {
			log.Printf("hasNode NoData err(%v), v(%v) hash(%#x)\n", err, v, hash)
			status = ErrNoData
			break
		}
	}

	if hr.ValidatorHash != nil {
		if v, err := s.bytesByHash.Get(hr.ValidatorHash); err != nil || v == nil {
			log.Printf("hasNode NoData err(%v), v(%v) hash(%#x)\n", err, v, hr.ValidatorHash)
			status = ErrNoData
		}
	}

	r := &result{hr.ReqID, status}
	s.log.Debugf("responseResult(%s) to peer(%s)\n", r, p)
	if b, err := c.MarshalToBytes(r); err != nil {
		s.log.Warnf("Failed to marshal result error(%+v)\n", err)
	} else if err = s.ph.Unicast(protoResult, b, p.id); err != nil {
		s.log.Infof("Failed to send result error(%+v)\n", err)
	}
}

func (s *server) _resolveNode(hashes [][]byte) (errCode, [][]byte) {
	values := make([][]byte, len(hashes))
	for i, hash := range hashes {
		var err error
		var v []byte
		for j, bucket := range []db.Bucket{s.merkleTrie, s.bytesByHash} {
			if v, err = bucket.Get(hash); err == nil && v != nil {
				values[i] = v
				break
			}
			s.log.Debugf("Cannot find value for (%#x) in (%d) bucket\n", hash, j)
			return ErrNoData, nil
		}
	}
	return NoError, values
}

func (s *server) requestNode(msg []byte, p *peer) {
	req := new(requestNodeData)
	if _, err := c.UnmarshalFromBytes(msg, &req); err != nil {
		s.log.Info("Failed to unmarshal error(%+v), (%#x)\n", err, msg)
		return
	}

	status, values := s._resolveNode(req.Hashes)
	res := &nodeData{req.ReqID, status, req.Type, values}
	b, err := c.MarshalToBytes(res)
	if err != nil {
		s.log.Warnf("Failed to marshal for nodeData(%v)\n", res)
		return
	}
	s.log.Debugf("responseNode (%s) to peer(%s)\n", res, p)
	if err = s.ph.Unicast(protoNodeData, b, p.id); err != nil {
		s.log.Info("Failed to send data peerID(%s)\n", p.id)
	}
}

func newServer(database db.Database, ph module.ProtocolHandler, log log.Logger) *server {
	mb, err := database.GetBucket(db.MerkleTrie)
	if err != nil {
		log.Panicf("Failed to get bucket for MerkleTrie err(%s)\n", err)
	}
	bb, err := database.GetBucket(db.BytesByHash)
	if err != nil {
		log.Panicf("Failed to get bucket for BytesByHash eerr(%s)\n", err)
	}
	s := &server{
		database:    database,
		ph:          ph,
		log:         log,
		merkleTrie:  mb,
		bytesByHash: bb,
	}
	return s
}