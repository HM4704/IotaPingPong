package main

import (
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/iotaledger/goshimmer/packages/consensus/gof"
	"github.com/iotaledger/goshimmer/packages/mana"
	"github.com/iotaledger/hive.go/identity"

	"github.com/iotaledger/goshimmer/client"
	"github.com/iotaledger/goshimmer/client/wallet"
	walletseed "github.com/iotaledger/goshimmer/client/wallet/packages/seed"
	"github.com/iotaledger/goshimmer/packages/ledgerstate"
)

func pingPong(clientUrl string, wg *sync.WaitGroup) (err error) {

	defer wg.Done()

	api := wallet.NewWebConnector(clientUrl)
	status, _ := api.ServerStatus()
	if !status.Synced {
		return
	}

	client := client.NewGoShimmerAPI(clientUrl, client.WithHTTPClient(http.Client{Timeout: 60 * time.Second}))

	fmt.Println("client %s started***\n", clientUrl)

	pingSeed := walletseed.NewSeed()

	pongSeed := walletseed.NewSeed()

	for {
		if _, err := client.SendFaucetRequest(pingSeed.Address(0).Base58(), -1); err != nil {
			fmt.Println(err)
		} else {
			break
		}
	}

	fmt.Println("faucet request sent\n")
	// wait for the funds
	out, err := WaitForFunds(client, pingSeed, 1, 0)
	if err != nil {
		fmt.Println("malformed OutputID")
		return
	}

	nodeId, err := mana.IDFromStr("E35sPFNueGQHgQCUFJPsz4mqqmFzD3tHbEho5H4nZTu7")
	// split utxo into 100
	count := 100
	outIDs := make([]ledgerstate.OutputID, count)
	{
		outs := make([]ledgerstate.Output, count)
		for l := 0; l < count; l++ {
			outs[l] = ledgerstate.NewSigLockedColoredOutput(ledgerstate.NewColoredBalances(map[ledgerstate.Color]uint64{
				ledgerstate.ColorIOTA: uint64(1000000 / count),
			}), pingSeed.Address(uint64(l+1)).Address())
		}
		txEssence := ledgerstate.NewTransactionEssence(0, time.Now(), nodeId, nodeId,
			ledgerstate.NewInputs(ledgerstate.NewUTXOInput(out[0])), ledgerstate.NewOutputs(outs...))
		kp := *pingSeed.KeyPair(0)
		sig := ledgerstate.NewED25519Signature(kp.PublicKey, kp.PrivateKey.Sign(txEssence.Bytes()))
		unlockBlock := ledgerstate.NewSignatureUnlockBlock(sig)
		tx := ledgerstate.NewTransaction(txEssence, ledgerstate.UnlockBlocks{unlockBlock})

		// issue the tx
		//fmt.Println("PostTransaction")

		for {
			_, err2 := client.PostTransaction(tx.Bytes())
			if err2 != nil {
				fmt.Println(err2)
				//				return
				continue
			}
			//fmt.Println(resp.TransactionID)
			break
		}
		outIDs, err = WaitForFunds(client, pingSeed, count, 1)
		if err != nil {
			fmt.Println("malformed OutputID")
			return
		}
	}

	for r := 0; r < 100; r++ {
		offs := r * count
		// send to pong
		fmt.Println("\n***** send to pong ******")
		PostTransactions(client, pongSeed, pingSeed, count, 1+offs, offs, outIDs, nodeId)
		if err != nil {
			fmt.Println("error in PostTransactions")
			return
		}
		outIDs, err = WaitForFunds(client, pongSeed, count, offs)
		if err != nil {
			fmt.Println("malformed OutputID")
			return
		}
		// send to ping
		fmt.Println("\n***** send to ping ******")
		PostTransactions(client, pingSeed, pongSeed, count, offs, 1+offs+count, outIDs, nodeId)
		if err != nil {
			fmt.Println("error in PostTransactions")
			return
		}
		outIDs, err = WaitForFunds(client, pingSeed, count, 1+offs+count)
		if err != nil {
			fmt.Println("malformed OutputID")
			return
		}
	}

	fmt.Println("\n************ FINISHED ****************\n")

	return
}

func main() {
	nbrNodes := flag.Int("nbrNodes", 5, "Number of nodes you want to test against")
	myNode := flag.String("node", "http://nodes.nectar.iota.cafe", "Valid node for initial transactions")

	nodeAPIURL := myNode
	//nodeAPIURL := "http://192.168.178.33:8080"
	//nodeAPIURL := "http://nodes.nectar.iota.cafe"

	//*nodeAPIURL = "http://localhost:8080"
	flag.Parse()
	fmt.Printf("spamming with %d nodes\n", *nbrNodes)

	nodes := GetNodes(GetRandomNode(*nodeAPIURL, 8 /* chose from all neighbours */), *nbrNodes)
	var wg sync.WaitGroup

	for _, node := range nodes {
		wg.Add(1)

		go pingPong(node, &wg)
	}
	wg.Wait()
}

func GetRandomNode(url string, numberOfNodes int) string {
	nodes := GetNodes(url, numberOfNodes)

	if nodes == nil {
		return ""
	}
	return nodes[rand.Intn(len(nodes))]
}

func GetNodes(url string, numberOfNodes int) []string {
	var nodes []string
	nodes = append(nodes, url)
	if numberOfNodes == 1 {
		return nodes
	}
	count := 0
	api := client.NewGoShimmerAPI(url, client.WithHTTPClient(http.Client{Timeout: 5 * time.Second}))

	data, _ := api.GetAutopeeringNeighbors(true)

	if data == nil {
		fmt.Println("error in GetNodes for ", url)
		return nil
	}
	for _, node := range data.KnownPeers {
		for _, service := range node.Services {
			if service.ID == "peering" {
				fmt.Printf("Checking url = %v\n", service.Address)
				api := wallet.NewWebConnector("http://" + strings.Split(service.Address, ":")[0] + ":8080")
				status, _ := api.ServerStatus()
				if status.Synced {
					fmt.Println(status)
					nodes = append(nodes, "http://"+strings.Split(service.Address, ":")[0]+":8080")
					count++
					if count == (numberOfNodes - 1) {
						return nodes
					}
				}
			}
		}

	}
	return nodes
}

func WaitForFunds(client *client.GoShimmerAPI, seed *walletseed.Seed, count int, addrOffs int) (outputID []ledgerstate.OutputID, err error) {
	var myOutputID string
	var confirmed bool
	outputID = make([]ledgerstate.OutputID, count)
	// wait for the funds
	fmt.Println("Waiting for funds to be confirmed...")
	for c := 0; c < count; c++ {
		for i := 0; i < 20000; i++ {
			resp, err := client.PostAddressUnspentOutputs([]string{seed.Address(uint64(c + addrOffs)).Base58()})
			if err != nil {
				fmt.Println(err)
				break
			}
			//fmt.Println("Waiting for funds to be confirmed...")
			for _, v := range resp.UnspentOutputs {
				if len(v.Outputs) > 0 {
					myOutputID = v.Outputs[0].Output.OutputID.Base58
					confirmed = (v.Outputs[0].GradeOfFinality == gof.High)
					break
				}
			}
			if myOutputID != "" && confirmed {
				//fmt.Println("funds confirmed...")
				break
			}
			time.Sleep(200 * time.Millisecond)
		}

		if myOutputID == "" {
			fmt.Println("Could not find OutputID")
			return nil, errors.New("Could not find OutputID")
		}

		if !confirmed {
			fmt.Println("OutputID not confirmed")
			return nil, errors.New("OutputID not confirmed")
		}

		outputID[c], err = ledgerstate.OutputIDFromBase58(myOutputID)
		if err != nil {
			return
		}
	}
	fmt.Println("funds confirmed...")
	return outputID, err
}

func PostTransactions(client *client.GoShimmerAPI, rcvSeed *walletseed.Seed, sndSeed *walletseed.Seed, count int, offs int, rcvOffs int, outIDs []ledgerstate.OutputID,
	nodeId identity.ID) (err error) {
	fmt.Println("PostTransactions")
	for l := 0; l < count; l++ {
		output := ledgerstate.NewSigLockedColoredOutput(ledgerstate.NewColoredBalances(map[ledgerstate.Color]uint64{
			ledgerstate.ColorIOTA: uint64(1000000 / count),
		}), rcvSeed.Address(uint64(l+rcvOffs)).Address())

		kp := *sndSeed.KeyPair(uint64(l + offs))

		// issue the tx
		//fmt.Println("PostTransaction")
		for {
			txEssence := ledgerstate.NewTransactionEssence(0, time.Now(), nodeId, nodeId,
				ledgerstate.NewInputs(ledgerstate.NewUTXOInput(outIDs[l])), ledgerstate.NewOutputs(output))
			sig := ledgerstate.NewED25519Signature(kp.PublicKey, kp.PrivateKey.Sign(txEssence.Bytes()))
			unlockBlock := ledgerstate.NewSignatureUnlockBlock(sig)
			tx := ledgerstate.NewTransaction(txEssence, ledgerstate.UnlockBlocks{unlockBlock})

			_, err := client.PostTransaction(tx.Bytes())
			if err != nil {
				fmt.Println(err)
				break
			} else {
				//fmt.Printf("issued transaction %s\n", resp.TransactionID)
				break
			}
		}
	}
	return err
}
