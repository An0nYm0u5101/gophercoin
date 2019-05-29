package gcd

import (
	"encoding/hex"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
)

const (
	defaultProtocolPort = "3000"
	defaultRPCHostPort  = "7777"
	defaultRESTHostPort = "7778"
	protocol            = "tcp"
	nodeVersion         = 1
	commandLength       = 12
)

// Server is the structure which defines the Gophercoin
// Daemon
type Server struct {
	cfg     Config
	db      *Blockchain
	wallet  *Wallet
	utxoSet *UTXOSet

	Router *mux.Router

	knownNodes      []Peer
	nodeAddress     string
	blocksInTransit [][]byte
	memPool         map[string]Transaction
	miningAddress   string
	miningTxs       bool

	wg *sync.WaitGroup

	quitChan     chan int
	minerChan    chan []byte
	timeChan     chan float64
	nodeServChan chan interface{}
}

// StartServer is the function used to start the gcd Server
func (s *Server) StartServer() {
	defer s.wg.Done()
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		log.Printf("[GCD] Catching signal, terminating gracefully.")
		if s.wallet != nil {
			s.wallet.SaveToFile()
		}

		os.Exit(1)
	}()
	// create a listener on TCP port
	var lis net.Listener

	if s.cfg.peerPort != "" {

		lst, err := net.Listen(protocol, ":"+s.cfg.peerPort)
		if err != nil {
			log.Printf("failed to listen: %v", err)
			return
		}
		lis = lst
	} else {
		lst, err := net.Listen(protocol, ":"+defaultProtocolPort)
		if err != nil {
			log.Printf("failed to listen: %v", err)
			return
		}
		lis = lst
	}

	log.Printf("[GCD] PeerServer listening on port %s", s.nodeAddress)

	if len(s.knownNodes) > 0 {
		if s.nodeAddress != s.knownNodes[0].Address {
			log.Printf("[PRSV] sending version message to %s\n", s.knownNodes[0].Address)
			s.sendVersion(s.knownNodes[0].Address)
		}
	}

	for {
		conn, err := lis.Accept()
		if err != nil {
			log.Panic(err)
		}
		go s.handleConnection(conn)
		s.wg.Add(1)
	}

}

// StartMiner is the function used to start the gcd Server
func (s *Server) StartMiner() {
	defer s.wg.Done()
	log.Printf("[GCMNR] Miner ready")
	go s.timeAdjustment()
	s.wg.Add(1)
	for {
		select {
		case <-s.quitChan:
			log.Printf("[GCMNR] Received stop signal")
			break
		case msg := <-s.minerChan:
			log.Printf("[GCMNR] Received tx with ID %v", msg)

			if len(s.memPool) > 2 {
				t := time.Now()
				s.miningTxs = true
				s.mineTxs()
				s.miningTxs = false
				now := time.Now()
				diff := now.Sub(t)
				log.Printf("[GCMNR] Mined new block after %v.", diff.Minutes())
			}

		case msg := <-s.timeChan:
			if msg > float64(2) && !s.miningTxs {
				t := time.Now()
				log.Printf("[GCMNR] %v minutes since last block, mining new.", msg)
				s.miningTxs = true
				s.mineTxs()
				s.miningTxs = false
				now := time.Now()
				diff := now.Sub(t)
				log.Printf("[GCMNR] Mined new block after %v.", diff.Minutes())
			}

		}

	}

}

func getExternalAddress() string {
	resp, err := http.Get("http://myexternalip.com/raw")
	if err != nil {
		log.Printf("[PRSV] Unable to fetch external ip address.")
		os.Exit(1)
	}
	defer resp.Body.Close()
	r, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[PRSV] Unable to read external ip address response.")
		os.Exit(1)
	}

	return string(r)
}

func (s *Server) mineTxs() {

	var txs []*Transaction
	for id := range s.memPool {
		tx := s.memPool[id]
		log.Printf("[GCMNR] Verifying transaction: %s\n", id)
		if s.db.VerifyTransaction(&tx) {
			log.Printf("[GCMNR] Verified transaction: %s\n", id)
			txs = append(txs, &tx)
		}
	}

	if len(txs) == 0 {
		log.Println("[GCMNR] No valid transactions in mempool")
	}
	var cbTx *Transaction
	if s.miningAddress == "" {
		s.miningAddress = s.wallet.CreateAddress()

		cbTx = NewCoinbaseTX(s.miningAddress, "")
	} else {
		cbTx = NewCoinbaseTX(s.miningAddress, "")
	}
	txs = append(txs, cbTx)

	newBlock := s.db.MineBlock(txs)
	s.utxoSet.Reindex()

	log.Println("[GCMNR] New block is mined!")

	for _, tx := range txs {
		txID := hex.EncodeToString(tx.ID)
		delete(s.memPool, txID)
	}

	for _, node := range s.knownNodes {
		if node.Address != s.nodeAddress {
			s.sendInv(node.Address, "block", [][]byte{newBlock.Hash})
		}
	}
}

func (s *Server) timeAdjustment() {
	defer s.wg.Done()

	for {
		if !s.miningTxs {
			if s.db != nil {
				tip := s.db.tip
				block, err := s.db.GetBlock(tip)
				if err != nil {
					log.Printf("[GCMNR] Unable to fetch blockchain tip.")
					os.Exit(1)
				}
				ts := time.Unix(0, block.Timestamp)
				now := time.Now()
				diff := now.Sub(ts)
				log.Printf("[GCMNR] Elapsed since last block %v.", diff.Minutes())
				if diff.Minutes() > float64(1) && !s.miningTxs {
					log.Printf("[GCMNR] Miner ping %v.", diff.Minutes())
					s.timeChan <- diff.Minutes()
					time.Sleep(600)
				} else {
					time.Sleep(600)
				}
			}
		}

	}

}

func (s *Server) timeSinceLastBlock() float64 {
	tip := s.db.tip
	block, err := s.db.GetBlock(tip)
	if err != nil {
		log.Printf("[GCMNR] Unable to fetch blockchain tip.")
		os.Exit(1)
	}

	t := time.Unix(0, block.Timestamp)
	elapsed := time.Since(t)
	if err != nil {
		log.Printf("[GCMNR] Elapsed since last block %v.", elapsed.Seconds())
		os.Exit(1)
	}

	return elapsed.Seconds()

}
