package network

import (
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/icon-project/goloop/common/log"
	"github.com/icon-project/goloop/common/wallet"

	"github.com/icon-project/goloop/common/crypto"
	"github.com/icon-project/goloop/module"
)

const (
	testChannel                = "testchannel"
	testTransportRandomAddress = ":0"
)

var (
	ProtoTestTransportRequest  = protocolInfo(0xF300)
	ProtoTestTransportResponse = protocolInfo(0xF400)
)

type testPeerHandler struct {
	*peerHandler
	t  *testing.T
	wg *sync.WaitGroup
}

func newTestPeerHandler(name string, t *testing.T, wg *sync.WaitGroup, l log.Logger) *testPeerHandler {
	return &testPeerHandler{newPeerHandler(l.WithFields(log.Fields{LoggerFieldKeySubModule: name})), t, wg}
}

type testTransportRequest struct {
	Message string
}

type testTransportResponse struct {
	Message string
}

func (ph *testPeerHandler) onPeer(p *Peer) {
	ph.logger.Println("onPeer", p)
	p.setPacketCbFunc(ph.onPacket)
	if !p.incomming {
		m := &testTransportRequest{Message: "Hello"}
		ph.sendMessage(ProtoTestTransportRequest, m, p)
		ph.logger.Println("sendProtoTestTransportRequest", m, p)
	}
}

func (ph *testPeerHandler) onError(err error, p *Peer, pkt *Packet) {
	ph.logger.Println("onError", err, p, pkt)
	ph.peerHandler.onError(err, p, pkt)
	assert.Fail(ph.t, "TestPeerHandler.onError", err.Error(), p, pkt)
}

func (ph *testPeerHandler) onPacket(pkt *Packet, p *Peer) {
	ph.logger.Println("onPacket", pkt, p)
	switch pkt.protocol {
	case PROTO_CONTOL:
		switch pkt.subProtocol {
		case ProtoTestTransportRequest:
			rm := &testTransportRequest{}
			ph.decode(pkt.payload, rm)
			ph.logger.Println("handleProtoTestTransportRequest", rm, p)

			m := &testTransportResponse{Message: "World"}
			ph.sendMessage(ProtoTestTransportResponse, m, p)

			ph.nextOnPeer(p)
		case ProtoTestTransportResponse:
			rm := &testTransportResponse{}
			ph.decode(pkt.payload, rm)
			ph.logger.Println("handleProtoTestTransportResponse", rm, p)

			ph.nextOnPeer(p)
			ph.wg.Done()
		}
	}
}

//Using mutex for prevent panic d.nx != 0
////crypto/sha256/sha256.go:253 (*digest).checkSum
////crypto/sha256/sha256.go:229 (*digest).Sum
////github.com/icon-project/goloop/vendor/github.com/haltingstate/secp256k1-go/secp256_rand.go:23 SumSHA256
////github.com/icon-project/goloop/vendor/github.com/haltingstate/secp256k1-go/secp256_rand.go:50 (*EntropyPool).Mix256
////github.com/icon-project/goloop/vendor/github.com/haltingstate/secp256k1-go/secp256_rand.go:71 (*EntropyPool).Mix
////github.com/icon-project/goloop/vendor/github.com/haltingstate/secp256k1-go/secp256_rand.go:133 RandByte
var walletMutex sync.Mutex

type testWallet struct {
	module.Wallet
}

func (w *testWallet) Sign(data []byte) ([]byte, error) {
	defer walletMutex.Unlock()
	walletMutex.Lock()
	return w.Wallet.Sign(data)
}

func walletFromGeneratedPrivateKey() module.Wallet {
	priK, _ := crypto.GenerateKeyPair()
	w, _ := wallet.NewFromPrivateKey(priK)
	return &testWallet{w}
}

func Test_transport(t *testing.T) {
	var wg sync.WaitGroup

	w1 := walletFromGeneratedPrivateKey()
	l1 := log.WithFields(log.Fields{
		log.FieldKeyWallet: hex.EncodeToString(w1.Address().ID()),
	})
	nt1 := NewTransport(testTransportRandomAddress, w1, l1)

	w2 := walletFromGeneratedPrivateKey()
	l2 := log.WithFields(log.Fields{
		log.FieldKeyWallet: hex.EncodeToString(w2.Address().ID()),
	})
	nt2 := NewTransport(testTransportRandomAddress, w2, l2)

	wg.Add(1)
	tph1 := newTestPeerHandler("TestPeerHandler1", t, &wg, nt1.(*transport).logger)
	tph2 := newTestPeerHandler("TestPeerHandler2", t, &wg, nt2.(*transport).logger)

	nt1.(*transport).pd.registerPeerHandler(tph1, false)
	nt2.(*transport).pd.registerPeerHandler(tph2, false)

	assert.NoError(t, nt1.Listen(), "Transport1.Start fail")
	assert.NoError(t, nt2.Listen(), "Transport2.Start fail")

	assert.NoError(t, nt2.Dial(nt1.GetListenAddress(), ""), "Transport.Dial fail")

	wg.Wait()
	assert.NoError(t, nt1.Close(), "Transport1.Close fail")
	assert.NoError(t, nt2.Close(), "Transport1.Close fail")
	time.Sleep(1 * time.Second)
}
