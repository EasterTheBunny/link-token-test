package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/smartcontractkit/chainlink/v2/core/gethwrappers/shared/generated/link_token"
)

const (
	mintAmount int64 = 100_000_000_000_000_000
	allowance  int64 = 10
	transfer   int64 = 6
)

type Config struct {
	HTTPURL            string `json:"http_url"`
	ChainID            int64  `json:"chain_id"`
	OwnerAddress       string `json:"owner_address"`
	OwnerPrivateKey    string `json:"owner_private_key"`
	ReceiverAddress    string `json:"receiver_address"`
	ReceiverPrivateKey string `json:"receiver_private_key"`
	DeployContract     bool   `json:"deploy_contract"`
	ContractAddress    string `json:"contract_address"`
	Mint               bool   `json:"mint"`
}

func main() {
	conf := mustReadConfig("config.json")

	rpcClient, err := rpc.Dial(conf.HTTPURL)
	if err != nil {
		log.Fatalf("dial rpc: %s", err.Error())
	}

	ownerClient := &client{
		rpc:     ethclient.NewClient(rpcClient),
		key:     privateKey(conf.OwnerPrivateKey),
		chainID: big.NewInt(conf.ChainID),
	}

	ctx := context.Background()

	var contract *link_token.LinkToken

	if conf.DeployContract {
		var err error

		if contract, err = deploy(ctx, ownerClient); err != nil {
			panic(err)
		}
	} else {
		var err error

		if contract, err = link_token.NewLinkToken(common.HexToAddress(conf.ContractAddress), ownerClient.rpc); err != nil {
			log.Fatalf("connect to token contract failure: %s", err.Error())
		}
	}

	if conf.Mint {
		if err := ensureMintingRoleFor(ctx, contract, ownerClient, conf.OwnerAddress); err != nil {
			panic(err)
		}

		if err := mintAmountTo(ctx, contract, ownerClient, mintAmount, conf.OwnerAddress); err != nil {
			panic(err)
		}
	}

	receiverClient := &client{
		rpc:     ethclient.NewClient(rpcClient),
		key:     privateKey(conf.ReceiverPrivateKey),
		chainID: big.NewInt(conf.ChainID),
	}

	fmt.Println("")
	fmt.Println("balance and approve")
	// print balance of sender and send
	mustPrintBalance(ctx, contract, conf.OwnerAddress)
	mustApprove(ctx, contract, ownerClient, allowance, conf.OwnerAddress, conf.ReceiverAddress)
	fmt.Println("")

	fmt.Println("balance and receive")
	// print balance of receiver and run
	mustPrintBalance(ctx, contract, conf.ReceiverAddress)
	mustReceive(ctx, contract, receiverClient, transfer, conf.OwnerAddress, conf.ReceiverAddress)
	fmt.Println("")

	fmt.Println("balances after")
	// print both balances after
	mustPrintBalance(ctx, contract, conf.OwnerAddress)
	mustPrintBalance(ctx, contract, conf.ReceiverAddress)
}

type client struct {
	rpc     *ethclient.Client
	key     *ecdsa.PrivateKey
	chainID *big.Int
}

func buildTxOpts(ctx context.Context, client *client) (*bind.TransactOpts, error) {
	var address common.Address

	nonce, err := client.rpc.PendingNonceAt(ctx, address)
	if err != nil {
		return nil, err
	}

	gasPrice, err := client.rpc.SuggestGasPrice(ctx)
	if err != nil {
		return nil, err
	}

	auth, err := bind.NewKeyedTransactorWithChainID(client.key, client.chainID)
	if err != nil {
		return nil, err
	}

	auth.Nonce = big.NewInt(int64(nonce))
	auth.Value = big.NewInt(0) // in wei
	// auth.GasLimit = 0
	auth.GasPrice = gasPrice

	return auth, nil
}

func deploy(ctx context.Context, client *client) (*link_token.LinkToken, error) {
	opts, err := buildTxOpts(ctx, client)
	if err != nil {
		return nil, err
	}

	addr, trx, contract, err := link_token.DeployLinkToken(opts, client.rpc)
	if err != nil {
		return nil, err
	}

	if _, err := bind.WaitDeployed(ctx, client.rpc, trx); err != nil {
		return nil, err
	}

	fmt.Println("contract address:", addr.Hex())

	return contract, nil
}

func ensureMintingRoleFor(ctx context.Context, contract *link_token.LinkToken, cl *client, addr string) error {
	minters, err := contract.GetMinters(&bind.CallOpts{Context: ctx})
	if err != nil {
		return err
	}

	var ownerIsMinter bool

	for _, minter := range minters {
		if minter.Hex() == addr {
			ownerIsMinter = true

			break
		}
	}

	if !ownerIsMinter {
		opts, err := buildTxOpts(ctx, cl)
		if err != nil {
			return err
		}

		trx, err := contract.GrantMintRole(opts, common.HexToAddress(addr))
		if err != nil {
			return err
		}

		receipt, err := bind.WaitMined(ctx, cl.rpc, trx)
		if err != nil {
			return err
		}

		if receipt.Status == types.ReceiptStatusFailed {
			return fmt.Errorf("failed status receipt: %d", receipt.Status)
		}
	}

	return nil
}

func mintAmountTo(ctx context.Context, contract *link_token.LinkToken, cl *client, amt int64, to string) error {
	opts, err := buildTxOpts(ctx, cl)
	if err != nil {
		return err
	}

	trx, err := contract.Mint(opts, common.HexToAddress(to), big.NewInt(amt))
	if err != nil {
		return err
	}

	receipt, err := bind.WaitMined(ctx, cl.rpc, trx)
	if err != nil {
		return err
	}

	if receipt.Status == types.ReceiptStatusFailed {
		return fmt.Errorf("failed status receipt: %d", receipt.Status)
	}

	return nil
}

func mustPrintBalance(ctx context.Context, contract *link_token.LinkToken, addr string) {
	balance, err := contract.BalanceOf(&bind.CallOpts{Context: ctx}, common.HexToAddress(addr))
	if err != nil {
		panic(err)
	}

	fmt.Printf("%s balance: %s juels\n", addr, balance.String())
}

func mustApprove(ctx context.Context, contract *link_token.LinkToken, cl *client, amt int64, from, to string) {
	opts, err := buildTxOpts(ctx, cl)
	if err != nil {
		panic(err)
	}

	trx, err := contract.Approve(opts, common.HexToAddress(to), big.NewInt(amt))
	if err != nil {
		panic(err)
	}

	receipt, err := bind.WaitMined(ctx, cl.rpc, trx)
	if err != nil {
		panic(err)
	}

	if receipt.Status == types.ReceiptStatusFailed {
		panic(fmt.Errorf("failed status receipt: %d", receipt.Status))
	}

	allowed, err := contract.Allowance(&bind.CallOpts{Context: ctx}, common.HexToAddress(from), common.HexToAddress(to))
	if err != nil {
		panic(err)
	}

	fmt.Printf("%s juels allowed from %s to %s\n", allowed.String(), from, to)
}

func mustReceive(ctx context.Context, contract *link_token.LinkToken, cl *client, amt int64, from, to string) {
	opts, err := buildTxOpts(ctx, cl)
	if err != nil {
		panic(err)
	}

	trx, err := contract.TransferFrom(opts, common.HexToAddress(from), common.HexToAddress(to), big.NewInt(amt))
	if err != nil {
		panic(err)
	}

	receipt, err := bind.WaitMined(ctx, cl.rpc, trx)
	if err != nil {
		panic(err)
	}

	if receipt.Status == types.ReceiptStatusFailed {
		panic(fmt.Errorf("failed status receipt: %d", receipt.Status))
	}

	allowed, err := contract.Allowance(&bind.CallOpts{Context: ctx}, common.HexToAddress(from), common.HexToAddress(to))
	if err != nil {
		panic(err)
	}

	fmt.Printf("%s juels allowed from %s to %s\n", allowed.String(), from, to)
}

func mustReadConfig(path string) Config {
	file, err := os.Open(path)
	if err != nil {
		panic(err)
	}

	confBytes, err := io.ReadAll(file)
	if err != nil {
		panic(err)
	}

	var conf Config
	if err := json.Unmarshal(confBytes, &conf); err != nil {
		panic(err)
	}

	return conf
}

func privateKey(key string) *ecdsa.PrivateKey {
	pkBase := new(big.Int).SetBytes(common.FromHex(strings.TrimSpace(key)))
	pkX, pkY := crypto.S256().ScalarBaseMult(pkBase.Bytes())

	return &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: crypto.S256(),
			X:     pkX,
			Y:     pkY,
		},
		D: pkBase,
	}
}
