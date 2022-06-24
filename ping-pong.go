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
	"github.com/iotaledger/goshimmer/packages/ledger/utxo"
	"github.com/iotaledger/goshimmer/packages/ledger/vm/devnetvm"
	"github.com/iotaledger/goshimmer/packages/mana"
	"github.com/iotaledger/hive.go/generics/lo"
	"github.com/iotaledger/hive.go/identity"

	"github.com/iotaledger/goshimmer/client"
	"github.com/iotaledger/goshimmer/client/wallet"
	walletseed "github.com/iotaledger/goshimmer/client/wallet/packages/seed"
)

func main() {
	nbrNodes := flag.Int("nbrNodes", 5, "Number of nodes you want to test against")
	myNode := flag.String("node", "http://nodes.nectar.iota.cafe", "Valid node for initial transactions")

	nodeAPIURL := myNode

	flag.Parse()
	fmt.Printf("spamming with %d nodes\n", *nbrNodes)

	nodes := GetNodes(GetRandomNode(*nodeAPIURL, 8 /* choose from all neighbors */), *nbrNodes)

	var wg sync.WaitGroup
	for _, node := range nodes {
		wg.Add(1)

		go PingPong(node, &wg)
		time.Sleep(2000 * time.Millisecond)
	}
	wg.Wait()
}

func PingPong(clientUrl string, wg *sync.WaitGroup) (err error) {

	defer wg.Done()

	client := client.NewGoShimmerAPI(clientUrl, client.WithHTTPClient(http.Client{Timeout: 60 * time.Second}))

	fmt.Printf("client %s started***\n", clientUrl)

	pingSeed := walletseed.NewSeed()

	pongSeed := walletseed.NewSeed()

	// fetch funds from faucet
	for {
		if _, err := client.BroadcastFaucetRequest(pingSeed.Address(0).Base58(), -1); err != nil {
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
	outIDs, err1 := SplitUTXO(client, pingSeed, out, count, nodeId)
	if err1 != nil {
		fmt.Println("Error from SplitUTXO ", err)
		return
	}

	loopcount := 100
	for r := 0; r < loopcount; r++ {
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

func IsNodeSynced(api *client.GoShimmerAPI) bool {
	info, err := api.Info()
	if (err == nil) && (info.TangleTime.Synced) {
		return true
	}
	return false
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

func SplitUTXO(client *client.GoShimmerAPI, pingSeed *walletseed.Seed, inputs []utxo.OutputID, count int, nodeId identity.ID) (outIDs []utxo.OutputID, err error) {
	outIDs = make([]utxo.OutputID, count)
	outs := make([]devnetvm.Output, count)
	for l := 0; l < count; l++ {
		outs[l] = devnetvm.NewSigLockedColoredOutput(devnetvm.NewColoredBalances(map[devnetvm.Color]uint64{
			devnetvm.ColorIOTA: uint64(1000000 / count),
		}), pingSeed.Address(uint64(l+1)).Address())
	}
	for {
		txEssence := devnetvm.NewTransactionEssence(0, time.Now(), nodeId, nodeId,
			devnetvm.NewInputs(devnetvm.NewUTXOInput(inputs[0])), devnetvm.NewOutputs(outs...))
		kp := *pingSeed.KeyPair(0)
		sig := devnetvm.NewED25519Signature(kp.PublicKey, kp.PrivateKey.Sign(lo.PanicOnErr(txEssence.Bytes())))
		//sig := devnetvm.NewED25519Signature(kp.PublicKey, kp.PrivateKey.Sign(txEssence.Bytes()))
		unlockBlock := devnetvm.NewSignatureUnlockBlock(sig)
		tx := devnetvm.NewTransaction(txEssence, devnetvm.UnlockBlocks{unlockBlock})

		_, err2 := client.PostTransaction(lo.PanicOnErr(tx.Bytes()))
		if err2 != nil {
			fmt.Println(err2)
			continue
		}
		break
	}
	outIDs, err = WaitForFunds(client, pingSeed, count, 1)
	if err != nil {
		fmt.Println("malformed OutputID")
		return
	}
	return outIDs, err
}

func WaitForFunds(client *client.GoShimmerAPI, seed *walletseed.Seed, count int, addrOffs int) (outputID []utxo.OutputID, err error) {
	var myOutputID string
	var confirmed bool
	outputID = make([]utxo.OutputID, count)
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

		err = outputID[c].FromBase58(myOutputID)
		if err != nil {
			return
		}
	}
	fmt.Println("funds confirmed...")
	return outputID, err
}

func PostTransactions(client *client.GoShimmerAPI, rcvSeed *walletseed.Seed, sndSeed *walletseed.Seed, count int, offs int, rcvOffs int, outIDs []utxo.OutputID,
	nodeId identity.ID) (err error) {
	fmt.Println("PostTransactions")
	for l := 0; l < count; l++ {
		output := devnetvm.NewSigLockedColoredOutput(devnetvm.NewColoredBalances(map[devnetvm.Color]uint64{
			devnetvm.ColorIOTA: uint64(1000000 / count),
		}), rcvSeed.Address(uint64(l+rcvOffs)).Address())

		kp := *sndSeed.KeyPair(uint64(l + offs))

		// issue the tx
		//fmt.Println("PostTransaction")
		for {
			txEssence := devnetvm.NewTransactionEssence(0, time.Now(), nodeId, nodeId,
				devnetvm.NewInputs(devnetvm.NewUTXOInput(outIDs[l])), devnetvm.NewOutputs(output))
			sig := devnetvm.NewED25519Signature(kp.PublicKey, kp.PrivateKey.Sign(lo.PanicOnErr(txEssence.Bytes())))
			//				sig := devnetvm.NewED25519Signature(kp.PublicKey, kp.PrivateKey.Sign(txEssence.Bytes()))
			unlockBlock := devnetvm.NewSignatureUnlockBlock(sig)
			tx := devnetvm.NewTransaction(txEssence, devnetvm.UnlockBlocks{unlockBlock})

			_, err := client.PostTransaction(lo.PanicOnErr(tx.Bytes()))
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
