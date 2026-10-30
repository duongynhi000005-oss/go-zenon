package vm

import (
	"bytes"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/zenon-network/go-zenon/chain"
	"github.com/zenon-network/go-zenon/chain/account"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/chain/store"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/consensus/api"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
)

// stubMomentumStore returns a fixed frontier momentum and nil for every
// other lookup, which is enough for setBlockMomentum to succeed when
// the template already carries a non-zero MomentumAcknowledged.
type stubMomentumStore struct {
	store.Momentum
	frontier   *nom.Momentum
	sendBlocks map[types.Hash]*nom.AccountBlock
}

func (s *stubMomentumStore) ChainIdentifier() uint64 { return 100 }

func (s *stubMomentumStore) GetFrontierMomentum() (*nom.Momentum, error) {
	return s.frontier, nil
}

func (s *stubMomentumStore) GetAccountBlockByHash(hash types.Hash) (*nom.AccountBlock, error) {
	if s.sendBlocks != nil {
		if b, ok := s.sendBlocks[hash]; ok {
			return b, nil
		}
	}
	return nil, nil
}

// stubChainForGenerate embeds chain.Chain and overrides the lookups
// that GenerateFromTemplate makes, so a test can force the nil-store
// panic path in newBlockContext without a real chain.
type stubChainForGenerate struct {
	chain.Chain
	momentumStore store.Momentum
	accountStore  store.Account
	cacheStore    store.Cache
}

func (c *stubChainForGenerate) ChainIdentifier() uint64 { return 1 }

func (c *stubChainForGenerate) GetFrontierMomentumStore() store.Momentum {
	return c.momentumStore
}

func (c *stubChainForGenerate) GetAccountStore(_ types.Address, _ types.HashHeight) store.Account {
	return c.accountStore
}

func (c *stubChainForGenerate) GetCacheStore(_ types.HashHeight) store.Cache {
	return c.cacheStore
}

// TestGenerateFromTemplate_NilAccountStoreRecover drives
// GenerateFromTemplate past setAll (frontier momentum readable,
// template fields set) and into newBlockContext, where the stub chain's
// GetAccountStore returns nil. newUserBlockContext panics on that nil;
// without the deferred recover the panic escapes to the producer
// goroutine's RecoverStack handler, which re-panics and ends the
// process. The recover must convert it to constants.ErrVmRunPanic and
// a nil transaction.
func TestGenerateFromTemplate_NilAccountStoreRecover(t *testing.T) {
	frontier := &nom.Momentum{
		Hash:   types.HexToHashPanic("01aab631b44daf47f68810a416bea4ed3695e89c0ab9668a0ab8acb834dfb900"),
		Height: 1,
	}
	chain := &stubChainForGenerate{
		momentumStore: &stubMomentumStore{frontier: frontier},
		accountStore:  nil,
		cacheStore:    nil,
	}
	s := NewSupervisor(chain, nil)

	template := &nom.AccountBlock{
		BlockType:            nom.BlockTypeUserSend,
		Address:              types.ParseAddressPanic("z1qzuej4d8292sstg38qpnca9tfpfmglqjryffpv"),
		ToAddress:            types.PillarContract,
		MomentumAcknowledged: frontier.Identifier(),
		PreviousHash:         types.HexToHashPanic("02aab631b44daf47f68810a416bea4ed3695e89c0ab9668a0ab8acb834dfb901"),
		Height:               2,
	}

	transaction, err := s.GenerateFromTemplate(template, nil)
	common.ExpectError(t, err, constants.ErrVmRunPanic)
	common.Expect(t, transaction, nil)
}

// TestGenerateFromTemplate_NilCacheStoreRecover is the cache-side twin:
// the account store resolves (a real in-memory store) but GetCacheStore
// returns nil, which newUserBlockContext also panics on. Same recover,
// same outcome.
func TestGenerateFromTemplate_NilCacheStoreRecover(t *testing.T) {
	userAddr := types.ParseAddressPanic("z1qzuej4d8292sstg38qpnca9tfpfmglqjryffpv")
	frontier := &nom.Momentum{
		Hash:   types.HexToHashPanic("01aab631b44daf47f68810a416bea4ed3695e89c0ab9668a0ab8acb834dfb900"),
		Height: 1,
	}
	chain := &stubChainForGenerate{
		momentumStore: &stubMomentumStore{frontier: frontier},
		accountStore:  account.NewAccountStore(userAddr, db.NewMemDB()),
		cacheStore:    nil,
	}
	s := NewSupervisor(chain, nil)

	template := &nom.AccountBlock{
		BlockType:            nom.BlockTypeUserSend,
		Address:              userAddr,
		ToAddress:            types.PillarContract,
		MomentumAcknowledged: frontier.Identifier(),
		PreviousHash:         types.HexToHashPanic("02aab631b44daf47f68810a416bea4ed3695e89c0ab9668a0ab8acb834dfb901"),
		Height:               2,
	}

	transaction, err := s.GenerateFromTemplate(template, nil)
	common.ExpectError(t, err, constants.ErrVmRunPanic)
	common.Expect(t, transaction, nil)
}

type vmRollbackStorageAccount struct {
	store.Account
	shouldFailPut func(key []byte) error
}

type vmRollbackPutDB struct {
	db.DB
	shouldFailPut func(key []byte) error
}

func (f *vmRollbackPutDB) Put(key, value []byte) error {
	if f.shouldFailPut != nil {
		if err := f.shouldFailPut(key); err != nil {
			return err
		}
	}
	return f.DB.Put(key, value)
}

func (f *vmRollbackStorageAccount) Storage() db.DB {
	return &vmRollbackPutDB{
		DB:            f.Account.Storage(),
		shouldFailPut: f.shouldFailPut,
	}
}

func (f *vmRollbackStorageAccount) Snapshot() store.Account {
	return &vmRollbackStorageAccount{
		Account:       f.Account.Snapshot(),
		shouldFailPut: f.shouldFailPut,
	}
}

type vmPillarTestContext struct {
	store.Account
	accountStoreSnapshot store.Account
	momentumStore        store.Momentum
	frontier             *nom.Momentum
	ticker               common.Ticker
	epochStats           map[uint64]*api.EpochStats
	pillarDelegations    map[uint64]map[string]*types.PillarDelegationDetail
}

func (ctx *vmPillarTestContext) CacheStore() store.Cache       { return nil }
func (ctx *vmPillarTestContext) MomentumStore() store.Momentum { return ctx.momentumStore }
func (ctx *vmPillarTestContext) GetFrontierMomentum() (*nom.Momentum, error) {
	return ctx.frontier, nil
}
func (ctx *vmPillarTestContext) GetGenesisMomentum() *nom.Momentum { return ctx.frontier }
func (ctx *vmPillarTestContext) Save() {
	s := ctx.Account.Snapshot()
	ctx.accountStoreSnapshot = ctx.Account
	ctx.Account = s
}
func (ctx *vmPillarTestContext) Reset() {
	ctx.Account = ctx.accountStoreSnapshot
	ctx.accountStoreSnapshot = nil
}
func (ctx *vmPillarTestContext) Done() {
	changes, _ := ctx.Account.Changes()
	ctx.Account = ctx.accountStoreSnapshot
	ctx.accountStoreSnapshot = nil
	common.DealWithErr(ctx.Account.Apply(changes))
}
func (ctx *vmPillarTestContext) AddBalance(ts *types.ZenonTokenStandard, amount *big.Int) {
	cur, err := ctx.GetBalance(*ts)
	common.DealWithErr(err)
	common.DealWithErr(ctx.SetBalance(*ts, new(big.Int).Add(cur, amount)))
}
func (ctx *vmPillarTestContext) SubBalance(ts *types.ZenonTokenStandard, amount *big.Int) {
	cur, err := ctx.GetBalance(*ts)
	common.DealWithErr(err)
	common.DealWithErr(ctx.SetBalance(*ts, new(big.Int).Sub(cur, amount)))
}
func (ctx *vmPillarTestContext) IsAcceleratorSporkEnforced() bool        { return true }
func (ctx *vmPillarTestContext) IsHtlcSporkEnforced() bool               { return true }
func (ctx *vmPillarTestContext) IsBridgeAndLiquiditySporkEnforced() bool { return true }
func (ctx *vmPillarTestContext) IsDynamicPlasmaSporkEnforced() bool      { return true }
func (ctx *vmPillarTestContext) IsPtlcSporkEnforced() bool               { return false }
func (ctx *vmPillarTestContext) GetPillarWeights() (map[string]*big.Int, error) {
	return nil, nil
}
func (ctx *vmPillarTestContext) EpochTicker() common.Ticker { return ctx.ticker }
func (ctx *vmPillarTestContext) EpochStats(epoch uint64) (*api.EpochStats, error) {
	return ctx.epochStats[epoch], nil
}
func (ctx *vmPillarTestContext) GetPillarDelegationsByEpoch(epoch uint64) (map[string]*types.PillarDelegationDetail, error) {
	return ctx.pillarDelegations[epoch], nil
}

// TestGenerateEmbeddedReceive_PillarWriteFailuresRollbackAtVMBoundary drives
// VM.generateEmbeddedReceive directly across the VM boundary for UpdatePillar
// (baseline terms write failure and new terms write failure) and Update
// (epoch-history write failure), asserting that VM.rollbackEmbedded produces
// a resultFail ContractReceive block and reverts all contract storage writes.
func TestGenerateEmbeddedReceive_PillarWriteFailuresRollbackAtVMBoundary(t *testing.T) {
	errInjected := errors.New("injected pillar db write failure")
	pillarName := "vm_boundary_pillar"
	stakeAddr := types.ParseAddressPanic("z1qzuej4d8292sstg38qpnca9tfpfmglqjryffpv")
	initProducer := types.ParseAddressPanic("z1qzal6c5s9rjnnxd2z7dvdhjxpmmj4fmw56a0mz")
	var newProducer types.Address
	newProducer[0] = types.UserAddrByte
	newProducer[1] = 77
	newProducer[19] = 77

	setup := func(t *testing.T, epoch uint64) (*VM, *vmPillarTestContext, *vmRollbackStorageAccount, *stubMomentumStore) {
		t.Helper()
		ticker := common.NewTicker(time.Unix(0, 0), 100*time.Second)
		_, epEnd := ticker.ToTime(epoch)
		ts := epEnd.Add(time.Duration(constants.RewardTimeLimit+20) * time.Second)
		frontier := &nom.Momentum{
			ChainIdentifier: 100,
			Hash:            types.HexToHashPanic("01aab631b44daf47f68810a416bea4ed3695e89c0ab9668a0ab8acb834dfb900"),
			Height:          constants.UpdateMinNumMomentums + 10,
			Timestamp:       &ts,
		}
		baseAcc := account.NewAccountStore(types.PillarContract, db.NewMemDB())
		fa := &vmRollbackStorageAccount{Account: baseAcc}

		initPillar := &definition.PillarInfo{
			Name:                         pillarName,
			BlockProducingAddress:        initProducer,
			RewardWithdrawAddress:        stakeAddr,
			StakeAddress:                 stakeAddr,
			Amount:                       constants.PillarStakeAmount,
			RegistrationTime:             100,
			RevokeTime:                   0,
			GiveBlockRewardPercentage:    50,
			GiveDelegateRewardPercentage: 50,
			PillarType:                   definition.NormalPillarType,
		}
		common.FailIfErr(t, initPillar.Save(fa.Storage()))
		common.FailIfErr(t, (&definition.ProducingPillar{Name: pillarName, Producing: &initProducer}).Save(fa.Storage()))

		ms := &stubMomentumStore{
			frontier:   frontier,
			sendBlocks: make(map[types.Hash]*nom.AccountBlock),
		}
		ctx := &vmPillarTestContext{
			Account:           fa,
			momentumStore:     ms,
			frontier:          frontier,
			ticker:            ticker,
			epochStats:        make(map[uint64]*api.EpochStats),
			pillarDelegations: make(map[uint64]map[string]*types.PillarDelegationDetail),
		}
		return NewVM(ctx, ms), ctx, fa, ms
	}

	baselineKey := definition.GetPillarEpochTermsKey(pillarName, 0)

	for _, tc := range []struct {
		name    string
		failKey func(ctx *vmPillarTestContext) []byte
	}{
		{
			name:    "baseline_terms_key",
			failKey: func(_ *vmPillarTestContext) []byte { return baselineKey },
		},
		{
			name: "new_terms_key",
			failKey: func(ctx *vmPillarTestContext) []byte {
				curEpoch := ctx.ticker.ToTick(*ctx.frontier.Timestamp)
				return definition.GetPillarEpochTermsKey(pillarName, curEpoch+1)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, ctx, fa, ms := setup(t, 0)
			targetKey := tc.failKey(ctx)
			fa.shouldFailPut = func(key []byte) error {
				if bytes.Equal(key, targetKey) {
					return errInjected
				}
				return nil
			}

			sendBlock := &nom.AccountBlock{
				Address:       stakeAddr,
				ToAddress:     types.PillarContract,
				BlockType:     nom.BlockTypeUserSend,
				Amount:        big.NewInt(0),
				TokenStandard: types.ZnnTokenStandard,
				Data: definition.ABIPillars.PackMethodPanic(
					definition.UpdatePillarMethodName,
					pillarName,
					newProducer,
					stakeAddr,
					uint8(80),
					uint8(10),
				),
			}
			sendBlock.Hash = sendBlock.ComputeHash()
			ms.sendBlocks[sendBlock.Hash] = sendBlock

			recvBlock, execErr, internalErr := v.generateEmbeddedReceive(sendBlock.Hash)
			if internalErr != nil {
				t.Fatalf("unexpected internalErr: %v", internalErr)
			}
			if !errors.Is(execErr, errInjected) {
				t.Fatalf("want execErr=%v, got %v", errInjected, execErr)
			}
			if !bytes.Equal(recvBlock.Data, common.Uint64ToBytes(resultFail)) {
				t.Fatalf("want resultFail in generated ContractReceive block, got %x", recvBlock.Data)
			}
			if _, err := definition.GetProducingPillarName(ctx.Storage(), newProducer); err != constants.ErrDataNonExistent {
				t.Fatalf("expected newProducer index rolled back, got err=%v", err)
			}
			if has, err := definition.HasPillarEpochTerms(ctx.Storage(), pillarName); err != nil || has {
				t.Fatalf("expected no PillarEpochTerms after VM rollback, got has=%v err=%v", has, err)
			}
		})
	}

	t.Run("epoch_history_key", func(t *testing.T) {
		v, ctx, fa, ms := setup(t, 0)
		ctx.pillarDelegations[0] = map[string]*types.PillarDelegationDetail{
			pillarName: {
				PillarDelegation: types.PillarDelegation{Name: pillarName},
				Backers:          map[types.Address]*big.Int{initProducer: big.NewInt(1000)},
			},
		}
		ctx.epochStats[0] = &api.EpochStats{
			Epoch:       0,
			TotalWeight: big.NewInt(1000),
			Pillars: map[string]*api.EpochPillarStats{
				pillarName: {
					Name:             pillarName,
					BlockNum:         10,
					ExpectedBlockNum: 10,
					Weight:           big.NewInt(1000),
				},
			},
		}

		histKey := definition.GetPillarEpochHistoryKey(0, pillarName)
		fa.shouldFailPut = func(key []byte) error {
			if bytes.Equal(key, histKey) {
				return errInjected
			}
			return nil
		}

		sendBlock := &nom.AccountBlock{
			Address:       stakeAddr,
			ToAddress:     types.PillarContract,
			BlockType:     nom.BlockTypeUserSend,
			Amount:        big.NewInt(0),
			TokenStandard: types.ZnnTokenStandard,
			Data:          definition.ABICommon.PackMethodPanic(definition.UpdateMethodName),
		}
		sendBlock.Hash = sendBlock.ComputeHash()
		ms.sendBlocks[sendBlock.Hash] = sendBlock

		recvBlock, execErr, internalErr := v.generateEmbeddedReceive(sendBlock.Hash)
		if internalErr != nil {
			t.Fatalf("unexpected internalErr: %v", internalErr)
		}
		if !errors.Is(execErr, errInjected) {
			t.Fatalf("want execErr=%v, got %v", errInjected, execErr)
		}
		if !bytes.Equal(recvBlock.Data, common.Uint64ToBytes(resultFail)) {
			t.Fatalf("want resultFail in generated ContractReceive block, got %x", recvBlock.Data)
		}
		lastEpoch, err := definition.GetLastEpochUpdate(ctx.Storage())
		common.FailIfErr(t, err)
		if lastEpoch.LastEpoch != -1 {
			t.Fatalf("expected LastEpochUpdate rolled back to -1, got %d", lastEpoch.LastEpoch)
		}
		dep, err := definition.GetRewardDeposit(ctx.Storage(), &stakeAddr)
		common.FailIfErr(t, err)
		if dep.Znn.Sign() != 0 {
			t.Fatalf("expected 0 ZNN credited after rollback, got %v", dep.Znn)
		}
	})
}
