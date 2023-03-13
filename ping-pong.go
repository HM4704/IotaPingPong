package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/iotaledger/goshimmer/packages/protocol/engine/ledger/utxo"
	"github.com/iotaledger/goshimmer/packages/protocol/engine/ledger/vm/devnetvm"
	"github.com/iotaledger/hive.go/crypto/identity"
	"github.com/iotaledger/hive.go/lo"

	//"github.com/iotaledger/hive.go/core/identity"

	"github.com/iotaledger/goshimmer/client"
	"github.com/iotaledger/goshimmer/client/wallet"
	walletseed "github.com/iotaledger/goshimmer/client/wallet/packages/seed"
	"github.com/juju/fslock"
)

const seedsFile = "wallets.dat"

var g_useRS = new(bool)

func main() {
	g_useRS = flag.Bool("useRS", true, "Use ratesetter")
	nbrNodes := flag.Int("nbrNodes", 5, "Number of nodes you want to test against")
	myNode := flag.String("node", "http://nodes.nectar.iota.cafe", "Valid node for initial transactions")

	nodeAPIURL := myNode

	flag.Parse()
	fmt.Printf("spamming with %d nodes using ratesetter(%v)\n", *nbrNodes, *g_useRS)

	nodes := GetNodes(GetRandomNode(*nodeAPIURL, 8 /* choose from all neighbors */), *nbrNodes, false)

	var wg sync.WaitGroup
	for _, node := range nodes {
		wg.Add(1)

		go PingPong(node, &wg)
		time.Sleep(2000 * time.Millisecond)
	}
	wg.Wait()
}

func PingPong(clientUrl string, wg *sync.WaitGroup) {

	defer wg.Done()

	client := client.NewGoShimmerAPI(clientUrl, client.WithHTTPClient(http.Client{Timeout: 60 * time.Second}))

	fmt.Printf("client %s started***\n", clientUrl)

	prepareFunds := false
	pingSeed := LoadSeed(seedsFile)
	//var pingSeed *walletseed.Seed = nil
	if pingSeed == nil {
		pingSeed = walletseed.NewSeed()
		prepareFunds = true
	} else {
		fmt.Printf("wallet loaded from file. address %s\n", pingSeed.Address(uint64(1)).Address().String())
	}

	pongSeed := walletseed.NewSeed()

	nodeId := identity.ID{}
	count := 100
	outIDs := make([]utxo.OutputID, 0)
	err := errors.New("no err")
	if prepareFunds {
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

		// split utxo into 100
		outIDs, err = SplitUTXO(client, pingSeed, out, count, nodeId)
		if err != nil {
			fmt.Println("Error from SplitUTXO ", err)
			return
		}
	} else {
		outIDs = make([]utxo.OutputID, count)
		for i := 0; i < count; i++ {
			out, err := WaitForFunds(client, pingSeed, 1, 1+i)
			if err != nil {
				fmt.Println("malformed OutputID")
				return
			}
			outIDs[i] = out[0]
		}
	}

	loopcount := 100
	for r := 0; r < loopcount; r++ {
		// send to pong
		fmt.Println("\n***** send to pong ******")
		err = PostTransactions(client, pongSeed, pingSeed, count, 1000000/count, 1, 0, outIDs, nodeId)
		if err != nil {
			fmt.Println("error in PostTransactions")
			return
		}
		outIDs, err = WaitForFunds(client, pongSeed, count, 0)
		if err != nil {
			fmt.Println("malformed OutputID")
			return
		}
		// send to ping
		fmt.Println("\n***** send to ping ******")
		err = PostTransactions(client, pingSeed, pongSeed, count, 1000000/count, 0, 1, outIDs, nodeId)
		if err != nil {
			fmt.Println("error in PostTransactions")
			return
		}
		outIDs, err = WaitForFunds(client, pingSeed, count, 1)
		if err != nil {
			fmt.Println("malformed OutputID")
			return
		}
	}

	SaveSeed(seedsFile, pingSeed)
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
	nodes := GetNodes(url, numberOfNodes, true)

	if nodes == nil {
		return ""
	}
	return nodes[rand.Intn(len(nodes))]
}

func GetNodes(url string, numberOfNodes int, noCheck bool) []string {
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
					ns := "http://" + strings.Split(service.Address, ":")[0] + ":8080"

					var accessMana int64 = 0
					if noCheck == false {
						client := client.NewGoShimmerAPI(ns, client.WithHTTPClient(http.Client{Timeout: 5 * time.Second}))
						mana, _ := client.GetManaFullIssuerID(status.ID)
						accessMana = mana.Access
					}

					if noCheck || accessMana > 9000000.000000 {
						nodes = append(nodes, ns)
						count++
						if noCheck == false {
							fmt.Printf("****** %s selected access mana=%f\n", ns, accessMana)
						}
						if count == (numberOfNodes - 1) {
							return nodes
						}
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
		SleepRateSetterEstimate(client)
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
					confirmed = v.Outputs[0].ConfirmationState.IsAccepted()
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

func PostTransactions(client *client.GoShimmerAPI, rcvSeed *walletseed.Seed, sndSeed *walletseed.Seed, count int, coins int, offs int, rcvOffs int, outIDs []utxo.OutputID,
	nodeId identity.ID) (err error) {
	fmt.Println("PostTransactions")
	for l := 0; l < count; l++ {
		output := devnetvm.NewSigLockedColoredOutput(devnetvm.NewColoredBalances(map[devnetvm.Color]uint64{
			devnetvm.ColorIOTA: uint64(coins),
		}), rcvSeed.Address(uint64(l+rcvOffs)).Address())

		kp := *sndSeed.KeyPair(uint64(l + offs))

		// issue the tx
		//fmt.Println("PostTransaction")
		for {
			SleepRateSetterEstimate(client)
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

func SleepRateSetterEstimate(client *client.GoShimmerAPI) error {
	if *g_useRS == true {
		res, err := client.RateSetter()
		if err != nil {
			fmt.Printf("Err %s in RateSetter()\n", err)
			return err
		}
		s := res.Estimate
		if res.Estimate > 0 {
			//			fmt.Printf("RS Estimate: %d\n", res.Estimate)
			if res.Estimate > 100000000000 {
				fmt.Printf("RS Estimate: %d. Limit to 5s\n", res.Estimate)
				s = 5 * 1000 * 1000 * 1000
			}

		}
		time.Sleep(s)
	}
	return nil
}

func SaveSeed(fileName string, seed *walletseed.Seed) {

	// lock the file
	lock := LockFile(fileName)
	if lock == nil {
		fmt.Println("failed to acquire lock ")
		return
	}

	// Open a new file for writing only
	file, err := os.OpenFile(
		fileName,
		os.O_WRONLY|os.O_CREATE|os.O_APPEND,
		0644|fs.ModeExclusive,
	)
	if err != nil {
		fmt.Println(err)
	}
	defer func() {
		file.Close()
		lock.Unlock()
	}()

	// append bytes to file
	byteSlice := seed.Bytes()
	_, err = file.Write(byteSlice)
	if err != nil {
		fmt.Println(err)
	}
}

func LockFile(fileName string) *fslock.Lock {
	var lock *fslock.Lock = nil
	for i := 0; i < 100; i++ {
		lock = fslock.New(fileName)
		lockErr := lock.TryLock()
		if lockErr != nil {
			//fmt.Println("falied to acquire lock > " + lockErr.Error())
			time.Sleep(100 * time.Millisecond)
		} else {
			break
		}
	}
	return lock
}

func LoadSeed(fileName string) *walletseed.Seed {

	seedSize := len(walletseed.NewSeed().Bytes())

	// lock the file
	lock := LockFile(fileName)
	if lock == nil {
		fmt.Println("failed to acquire lock ")
		return nil
	}

	// Open file for reading
	file, err := os.OpenFile(fileName, os.O_RDWR, fs.ModeExclusive)
	if err != nil {
		fmt.Println(err)
		return nil
	}
	defer func() {
		file.Close()
		lock.Unlock()
	}()

	data, err := ioutil.ReadAll(file)
	if err != nil {
		fmt.Println(err)
		return nil
	}

	len := len(data)
	if len >= seedSize {
		// use the first seed (32 bytes), truncate and save the rest
		seedBytes := data[0:seedSize]

		if err := os.Truncate(fileName, 0); err != nil {
			fmt.Printf("Failed to truncate: %v", err)
		}
		if len > seedSize {
			file.Seek(0, 0)
			d := data[seedSize:len]
			_, err = file.Write(d)
			if err != nil {
				fmt.Println(err)
			}
		}
		return walletseed.NewSeed(seedBytes)
	}
	return nil
}
