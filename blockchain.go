package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"os"

	"github.com/boltdb/bolt"
)

// Blockchain is an array of blocks.
// Arrays in Go are ordered by default,
// which helps with some minor issues
type Blockchain struct {
	tip []byte
	db  *bolt.DB
}

// DBExists is used to check if the database
// already exists locally or not
func DBExists() bool {
	if _, err := os.Stat(dbFile); os.IsNotExist(err) {
		return false
	}

	return true
}

// MineBlock is the method used to mine a block
// with the provided transactions. The parameter `transactions`
// passed as a pointer to a slice of transactions
func (bc *Blockchain) MineBlock(transactions []*Transaction) {
	var lastHash []byte

	err := bc.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(blocksBucket))
		lastHash = b.Get([]byte("1"))

		return nil
	})
	if err != nil {
		fmt.Printf("Error getting last block")
	}

	newBlock := NewBlock([]byte(lastHash), transactions)

	err = bc.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(blocksBucket))
		block, err := newBlock.SerializeBlock()
		if err != nil {
			fmt.Printf("Error serializing new block")
			return nil
		}
		err = b.Put(newBlock.Hash, block)
		if err != nil {
			fmt.Printf("Error updating bucket with new block")
			return nil
		}
		err = b.Put([]byte("1"), newBlock.Hash)
		if err != nil {
			fmt.Printf("Error serializing genesis block")
			return nil
		}
		bc.tip = newBlock.Hash

		return nil
	})
}

// BlockchainIterator is the struct defining
// the iterator used to iterate over all the keys
// in a boltdb bucket
type BlockchainIterator struct {
	currentHash []byte
	db          *bolt.DB
}

//Iterator is the method used to create an iterator,
// it will be linked to the blockchain tip
func (bc *Blockchain) Iterator() *BlockchainIterator {
	bci := &BlockchainIterator{bc.tip, bc.db}

	return bci
}

// Next is the method used to get the next block
// while iterating the blockchain
func (i *BlockchainIterator) Next() *Block {
	var block *Block

	err := i.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(blocksBucket))
		encodedBlock := b.Get(i.currentHash)
		nblock, err := DeserializeBlock(encodedBlock)
		if err != nil {
			fmt.Printf("Error deserializing block")
			return nil
		}
		block = nblock
		return nil
	})
	if err != nil {
		fmt.Printf("Error serializing new block")

	}

	i.currentHash = block.PrevBlockHash

	return block
}

// FindUnspentTransactions returns a list of transactions containing unspent outputs
func (bc *Blockchain) FindUnspentTransactions(address string) []Transaction {
	var unspentTXs []Transaction
	spentTXOs := make(map[string][]int)
	bci := bc.Iterator()

	for {
		block := bci.Next()

		for _, tx := range block.Transactions {
			txID := hex.EncodeToString(tx.ID)

		Outputs:
			for outIdx, out := range tx.Vout {
				// Was the output spent?
				if spentTXOs[txID] != nil {
					for _, spentOut := range spentTXOs[txID] {
						if spentOut == outIdx {
							continue Outputs
						}
					}
				}

				if out.CanBeUnlockedWith(address) {
					unspentTXs = append(unspentTXs, *tx)
				}
			}

			if tx.IsCoinbase() == false {
				for _, in := range tx.Vin {
					if in.CanUnlockOutputWith(address) {
						inTxID := hex.EncodeToString(in.Txid)
						spentTXOs[inTxID] = append(spentTXOs[inTxID], in.Vout)
					}
				}
			}
		}

		if len(block.PrevBlockHash) == 0 {
			break
		}
	}

	return unspentTXs
}

// FindUTXO finds and returns all unspent transaction outputs
func (bc *Blockchain) FindUTXO(address string) []TXOutput {
	var UTXOs []TXOutput
	unspentTransactions := bc.FindUnspentTransactions(address)

	for _, tx := range unspentTransactions {
		for _, out := range tx.Vout {
			if out.CanBeUnlockedWith(address) {
				UTXOs = append(UTXOs, out)
			}
		}
	}

	return UTXOs
}

// FindSpendableOutputs finds and returns unspent outputs to reference in inputs
func (bc *Blockchain) FindSpendableOutputs(address string, amount int) (int, map[string][]int) {
	unspentOutputs := make(map[string][]int)
	unspentTXs := bc.FindUnspentTransactions(address)
	accumulated := 0

Work:
	for _, tx := range unspentTXs {
		txID := hex.EncodeToString(tx.ID)

		for outIdx, out := range tx.Vout {
			if out.CanBeUnlockedWith(address) && accumulated < amount {
				accumulated += out.Value
				unspentOutputs[txID] = append(unspentOutputs[txID], outIdx)

				if accumulated >= amount {
					break Work
				}
			}
		}
	}

	return accumulated, unspentOutputs
}

// CreateBlockchain creates a new blockchain DB
func CreateBlockchain(address string) *Blockchain {

	if DBExists() {
		fmt.Println("Blockchain already exists.")
		os.Exit(1)
	}

	var tip []byte
	db, err := bolt.Open(dbFile, 0600, nil)
	if err != nil {
		log.Panic(err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		cbtx := NewCoinbaseTX(address, genesisCoinbaseData)
		genesis := genesisBlock(cbtx)

		b, err := tx.CreateBucket([]byte(blocksBucket))
		if err != nil {
			log.Panic(err)
		}
		ser, err := genesis.SerializeBlock()
		if err != nil {
			log.Panic(err)
		}
		err = b.Put(genesis.Hash, ser)
		if err != nil {
			log.Panic(err)
		}

		err = b.Put([]byte("l"), genesis.Hash)
		if err != nil {
			log.Panic(err)
		}
		tip = genesis.Hash

		return nil
	})

	if err != nil {
		log.Panic(err)
	}

	bc := Blockchain{
		tip: tip,
		db:  db,
	}

	return &bc
}

// NewBlockchain is used to open a db file,
// check if a Blockchain already existed,
// if so gets the current blockchain tip,
// else generates the genesis block and
// sets it as the tip
func NewBlockchain(data []byte) *Blockchain {

	if DBExists() == false {
		fmt.Println("No existing blockchain found. Create one first.")
		os.Exit(1)
	}

	var tip []byte
	db, err := bolt.Open(dbFile, 0600, nil)
	if err != nil {
		fmt.Printf("Error opening db file while creating blockchain")
		return nil
	}
	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(blocksBucket))
		tip = b.Get([]byte("1"))
		return nil
	})

	if err != nil {
		log.Panic(err)
	}

	bc := Blockchain{
		tip: tip,
		db:  db,
	}

	return &bc
}