package nsb

import (
	"errors"
	"fmt"
	system_merkle_proof "github.com/HyperService-Consortium/NSB/contract/system/merkle-proof"

	"github.com/HyperService-Consortium/NSB/application/response"
	transactiontype "github.com/HyperService-Consortium/NSB/application/transaction-type"
	"github.com/HyperService-Consortium/NSB/merkmap"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/version"
	dbm "github.com/tendermint/tm-db"

	log "github.com/HyperService-Consortium/NSB/log"
	"time"
)

func NewNSBApplication(logger log.TendermintLogger, dbDir string) (*NSBApplication, error) {
	name := "nsbstate"
	db, err := dbm.NewGoLevelDB(name, dbDir)
	if err != nil {
		return nil, err
	}
	state := loadState(db)
	logger.Info("loading state...", "state_root", state.StateRoot, "height", state.Height)
	state.Reset()

	var stmp *merkmap.MerkMap
	var statedb *leveldb.DB
	statedb, err = leveldb.OpenFile(dbDir+"trienode.db", nil)
	if err != nil {
		return nil, err
	}
	// stmp, err = merkmap.NewMerkMapFromDB(statedb, state.StateRoot, "00")
	stmp, err = merkmap.NewMerkMapFromDB(statedb, "00", "00")
	if err != nil {
		return nil, err
	}

	app := &NSBApplication{
		state:                      state,
		logger:                     logger,
		stateMap:                   stmp,
		accMap:                     stmp.ArrangeSlot(accMapSlot),
		txMap:                      stmp.ArrangeSlot(txMapSlot),
		actionMap:                  stmp.ArrangeSlot(actionMapSlot),
		validMerkleProofMap:        stmp.ArrangeSlot(validMerkleProofMapSlot),
		validOnchainMerkleProofMap: stmp.ArrangeSlot(validOnchainMerkleProofMapSlot),
		statedb:                    statedb,
	}

	app.system.merkleProof = system_merkle_proof.NewContract(
		(*system_merkle_proof.ValidMerkleProofMap)(app.validMerkleProofMap),
		(*system_merkle_proof.ValidOnChainMerkleProofMap)(app.validOnchainMerkleProofMap))

	return app, nil
}

func (nsb *NSBApplication) SetLogger(l log.TendermintLogger) {
	nsb.logger = l
}

func (nsb *NSBApplication) Revert() error {
	err := nsb.stateMap.Revert()
	if err != nil {
		return err
	}
	nsb.accMap = nsb.stateMap.ArrangeSlot(accMapSlot)
	nsb.txMap = nsb.stateMap.ArrangeSlot(txMapSlot)
	nsb.actionMap = nsb.stateMap.ArrangeSlot(actionMapSlot)
	nsb.validMerkleProofMap = nsb.stateMap.ArrangeSlot(validMerkleProofMapSlot)
	nsb.validOnchainMerkleProofMap = nsb.stateMap.ArrangeSlot(validOnchainMerkleProofMapSlot)

	return nil
}

func (nsb *NSBApplication) Info(req types.RequestInfo) types.ResponseInfo {
	return types.ResponseInfo{
		Version:          version.ABCIVersion,
		LastBlockAppHash: nsb.state.StateRoot,
		LastBlockHeight:  nsb.state.Height,
		AppVersion:       NSBVersion.Uint64(),
	}
}

// InitChain Save the validators in the merkle tree
func (nsb *NSBApplication) InitChain(req types.RequestInitChain) types.ResponseInitChain {
	nsb.logger.Info("InitChain")
	for _, v := range req.Validators {
		r := nsb.updateValidator(v)
		if r.IsErr() {
			nsb.logger.Error("Error updating validators", "r", r)
		}
	}
	return types.ResponseInitChain{
		ConsensusParams: &types.ConsensusParams{
			Block: &types.BlockParams{
				MaxBytes: 66060288,
				MaxGas:   1024,
			},
			Evidence: &types.EvidenceParams{
				MaxAge: 100,
			},
			Validator: &types.ValidatorParams{
				PubKeyTypes: []string{"ed25519"},
			},
		},
		Validators: nsb.ValUpdates,
	}
}

// Track the block hash and header information
func (nsb *NSBApplication) BeginBlock(req types.RequestBeginBlock) types.ResponseBeginBlock {
	// reset valset changes
	nsb.logger.Info("BeginBlock")
	nsb.ValUpdates = nsb.ValUpdates[:0]
	return types.ResponseBeginBlock{}
}

// Update the validator set
func (nsb *NSBApplication) EndBlock(req types.RequestEndBlock) types.ResponseEndBlock {
	nsb.logger.Info("EndBlock")
	return types.ResponseEndBlock{ValidatorUpdates: nsb.ValUpdates}
}

func (nsb *NSBApplication) CheckTx(types.RequestCheckTx) types.ResponseCheckTx {
	nsb.logger.Info("CheckTx")
	return types.ResponseCheckTx{Code: 0}
}

func (nsb *NSBApplication) DeliverTx(req types.RequestDeliverTx) types.ResponseDeliverTx {
	nsb.logger.Info("DeliverTx")

	// be used here to simulate the delay......
	time.Sleep(1 * time.Nanosecond)
	//time.Sleep( * time.Second)

	if len(req.Tx) < 1 {
		return *response.InvalidTxInputFormatTooShort
	}
	var ret *types.ResponseDeliverTx
	switch req.Tx[0] {
	case transactiontype.Validators: // nsb validators
		ret = nsb.execValidatorTx(req.Tx[1:])

	case transactiontype.SendTransaction: // transact contract methods
		ret = nsb.parseFuncTransaction(req.Tx[1:])

	case transactiontype.SystemCall: // transact system contract methods
		ret = nsb.parseSystemFuncTransaction(req.Tx[1:]) //define what transactions do.

	case transactiontype.CreateContract: // create on-chain contracts
		ret = nsb.parseCreateTransaction(req.Tx[1:])

	default:
		return types.ResponseDeliverTx{Code: uint32(response.CodeInvalidTxType())}
	}
	if ret.Code != uint32(response.CodeOK()) {
		err := nsb.Revert()
		if err != nil {
			nsb.logger.Error("Revert Error", "error", err)
		}
	}
	return *ret
}

func (nsb *NSBApplication) Commit() types.ResponseCommit {
	nsb.logger.Info("Commit")
	var err error

	// accMap, txMap is the sub-Map of stateMap
	nsb.state.StateRoot, err = nsb.stateMap.Commit(nil)
	if err != nil {
		nsb.logger.Info("Commit error!!!!!!!!!!!!!!!!!!!!!!!!!!")
		panic(err)
	}
	nsb.state.Height += 1
	saveState(nsb.state)
	return types.ResponseCommit{Data: nsb.state.StateRoot}
}

/*
type RequestQuery struct {
    Data                 []byte   `protobuf:"bytes,1,opt,name=data,proto3" json:"data,omitempty"`
    Path                 string   `protobuf:"bytes,2,opt,name=path,proto3" json:"path,omitempty"`
    Height               int64    `protobuf:"varint,3,opt,name=height,proto3" json:"height,omitempty"`
    Prove                bool     `protobuf:"varint,4,opt,name=prove,proto3" json:"prove,omitempty"`
}
type ResponseQuery struct {
    Code uint32 `protobuf:"varint,1,opt,name=code,proto3" json:"code,omitempty"`
    // bytes data = 2; // use "value" instead.
    Log                  string        `protobuf:"bytes,3,opt,name=log,proto3" json:"log,omitempty"`
    Info                 string        `protobuf:"bytes,4,opt,name=info,proto3" json:"info,omitempty"`
    Index                int64         `protobuf:"varint,5,opt,name=index,proto3" json:"index,omitempty"`
    Key                  []byte        `protobuf:"bytes,6,opt,name=key,proto3" json:"key,omitempty"`
    Value                []byte        `protobuf:"bytes,7,opt,name=value,proto3" json:"value,omitempty"`
    Proof                *merkle.Proof `protobuf:"bytes,8,opt,name=proof" json:"proof,omitempty"`
    Height               int64         `protobuf:"varint,9,opt,name=height,proto3" json:"height,omitempty"`
    Codespace            string        `protobuf:"bytes,10,opt,name=codespace,proto3" json:"codespace,omitempty"`
}
type Proof struct {
    Ops                  []ProofOp `protobuf:"bytes,1,rep,name=ops" json:"ops"`
}
*/
func (nsb *NSBApplication) Query(req types.RequestQuery) (ret types.ResponseQuery) {
	ret.Key = req.Data
	ret.Value = []byte(req.Path)
	ret.Height = nsb.state.Height
	ret.Log = fmt.Sprintf("asking not Prove key: %v, value %v", req.Data, req.Path)
	ret.Code, ret.Info = nsb.QueryIndex(&req)
	return
}

func (nsb *NSBApplication) Stop() (err1 error, err2 error) {
	if nsb == nil {
		return errors.New("the NSB application is not started"), nil
	}
	err1 = nsb.state.Close()
	err2 = nsb.stateMap.Close()
	nsb.state = nil
	nsb.stateMap = nil
	return
}
