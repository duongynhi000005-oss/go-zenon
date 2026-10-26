package implementation

import (
	"bytes"
	"errors"
	"math/big"
	"testing"
	"time"

	accountstore "github.com/zenon-network/go-zenon/chain/account"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/chain/store"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/consensus/api"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
)

type testAcceleratorMomentumStore struct {
	store.Momentum
	pillars []*definition.PillarInfo
}

func (m *testAcceleratorMomentumStore) GetActivePillars() ([]*definition.PillarInfo, error) {
	return m.pillars, nil
}

type failingKeyDB struct {
	db.DB
	failKey []byte
	failErr error
}

func (f *failingKeyDB) Put(key, value []byte) error {
	if bytes.Equal(key, f.failKey) {
		return f.failErr
	}
	return f.DB.Put(key, value)
}

type testAcceleratorContext struct {
	store.Account
	storage       db.DB
	momentumStore store.Momentum
	genesis       *nom.Momentum
	frontier      *nom.Momentum
	dpEnforced    bool
}

func newTestAcceleratorContext(dpEnforced bool) *testAcceleratorContext {
	genesisTime := time.Unix(1000000000, 0)
	frontierTime := time.Unix(1000000200, 0)
	memDB := db.NewMemDB()
	acct := accountstore.NewAccountStore(types.AcceleratorContract, memDB)
	p1Addr := types.ParseAddressPanic("z1qqq43dyrswfehx9w9td43exflqzcxrt7g6alah")
	p2Addr := types.ParseAddressPanic("z1qz8v73ea2vy2rrlq7skssngu8cm8mknjjkr2ju")
	p3Addr := types.ParseAddressPanic("z1qr4pexnnfaexqqz8nscjjcsajy5hdqfkgadvwx")
	return &testAcceleratorContext{
		Account: acct,
		storage: acct.Storage(),
		momentumStore: &testAcceleratorMomentumStore{
			pillars: []*definition.PillarInfo{
				{Name: "Pillar1", StakeAddress: p1Addr, BlockProducingAddress: p1Addr},
				{Name: "Pillar2", StakeAddress: p2Addr, BlockProducingAddress: p2Addr},
				{Name: "Pillar3", StakeAddress: p3Addr, BlockProducingAddress: p3Addr},
			},
		},
		genesis: &nom.Momentum{
			ChainIdentifier: 100,
			Timestamp:       &genesisTime,
			Height:          1,
		},
		frontier: &nom.Momentum{
			ChainIdentifier: 100,
			Timestamp:       &frontierTime,
			Height:          10,
		},
		dpEnforced: dpEnforced,
	}
}

func (ctx *testAcceleratorContext) Storage() db.DB                        { return ctx.storage }
func (ctx *testAcceleratorContext) CacheStore() store.Cache               { return nil }
func (ctx *testAcceleratorContext) MomentumStore() store.Momentum         { return ctx.momentumStore }
func (ctx *testAcceleratorContext) GetFrontierMomentum() (*nom.Momentum, error) {
	return ctx.frontier, nil
}
func (ctx *testAcceleratorContext) GetGenesisMomentum() *nom.Momentum { return ctx.genesis }

func (ctx *testAcceleratorContext) Save()  {}
func (ctx *testAcceleratorContext) Reset() {}
func (ctx *testAcceleratorContext) Done()  {}

func (ctx *testAcceleratorContext) AddBalance(ts *types.ZenonTokenStandard, amount *big.Int) {}
func (ctx *testAcceleratorContext) SubBalance(ts *types.ZenonTokenStandard, amount *big.Int) {}

func (ctx *testAcceleratorContext) IsAcceleratorSporkEnforced() bool        { return true }
func (ctx *testAcceleratorContext) IsHtlcSporkEnforced() bool               { return true }
func (ctx *testAcceleratorContext) IsBridgeAndLiquiditySporkEnforced() bool { return true }
func (ctx *testAcceleratorContext) IsDynamicPlasmaSporkEnforced() bool      { return ctx.dpEnforced }
func (ctx *testAcceleratorContext) IsPtlcSporkEnforced() bool               { return false }

func (ctx *testAcceleratorContext) GetPillarWeights() (map[string]*big.Int, error) {
	return nil, nil
}
func (ctx *testAcceleratorContext) EpochTicker() common.Ticker {
	return common.NewTicker(time.Unix(0, 0), time.Second)
}
func (ctx *testAcceleratorContext) EpochStats(epoch uint64) (*api.EpochStats, error) {
	return nil, nil
}
func (ctx *testAcceleratorContext) GetPillarDelegationsByEpoch(epoch uint64) (map[string]*types.PillarDelegationDetail, error) {
	return nil, nil
}

func (ctx *testAcceleratorContext) setFrontier(height uint64, unixSec int64) {
	t := time.Unix(unixSec, 0)
	ctx.frontier = &nom.Momentum{
		ChainIdentifier: 100,
		Timestamp:       &t,
		Height:          height,
	}
}

func seedProject(ctx *testAcceleratorContext, id types.Hash, creationTimestamp int64) *definition.Project {
	owner := types.ParseAddressPanic("z1qzal6c5s9rjnnxd2z7dvdhjxpmmj4fmw56a0mz")
	project := &definition.Project{
		Id:                  id,
		Owner:               owner,
		Name:                "Project",
		Description:         "Description",
		Url:                 "example.com",
		ZnnFundsNeeded:      big.NewInt(100),
		QsrFundsNeeded:      big.NewInt(1000),
		CreationTimestamp:   creationTimestamp,
		LastUpdateTimestamp: creationTimestamp,
		Status:              definition.VotingStatus,
		PhaseIds:            []types.Hash{},
	}
	project.Save(ctx.Storage())
	(&definition.VotableHash{Id: id}).Save(ctx.Storage())
	return project
}

func TestAccelerator_ElapsedVotingWindowFinalTally(t *testing.T) {
	const createTime = int64(1000000200)
	expiry := createTime + constants.AcceleratorProjectVotingPeriod

	t.Run("post-spork passing breakdown after window elapses activates project", func(t *testing.T) {
		ctx := newTestAcceleratorContext(true)
		projectID := types.HexToHashPanic("0101010101010101010101010101010101010101010101010101010101010101")
		seedProject(ctx, projectID, createTime)

		(&definition.PillarVote{Id: projectID, Name: "Pillar1", Vote: definition.VoteYes}).Save(ctx.Storage())
		(&definition.PillarVote{Id: projectID, Name: "Pillar2", Vote: definition.VoteYes}).Save(ctx.Storage())

		ctx.setFrontier(constants.UpdateMinNumMomentums+1, expiry+1)
		updateBlock := &nom.AccountBlock{
			Address:   types.AcceleratorContract,
			ToAddress: types.AcceleratorContract,
			Data:      definition.ABIAccelerator.PackMethodPanic(definition.UpdateMethodName),
			Amount:    big.NewInt(0),
		}
		method := &UpdateEmbeddedAcceleratorMethod{MethodName: definition.UpdateMethodName}
		if _, err := method.ReceiveBlock(ctx, updateBlock); err != nil {
			t.Fatalf("unexpected Update error: %v", err)
		}

		stored, err := definition.GetProjectEntry(ctx.Storage(), projectID)
		if err != nil {
			t.Fatalf("unexpected GetProjectEntry error: %v", err)
		}
		if stored.Status != definition.ActiveStatus {
			t.Fatalf("want status %d (ActiveStatus), got %d", definition.ActiveStatus, stored.Status)
		}
		if stored.LastUpdateTimestamp != expiry+1 {
			t.Fatalf("want LastUpdateTimestamp %d, got %d", expiry+1, stored.LastUpdateTimestamp)
		}
	})

	t.Run("post-spork failing breakdown after window elapses closes project", func(t *testing.T) {
		ctx := newTestAcceleratorContext(true)
		projectID := types.HexToHashPanic("0202020202020202020202020202020202020202020202020202020202020202")
		seedProject(ctx, projectID, createTime)

		// 1 of 3 pillars (33.3% > 33%), but VoteNo beats VoteYes.
		(&definition.PillarVote{Id: projectID, Name: "Pillar1", Vote: definition.VoteNo}).Save(ctx.Storage())
		(&definition.PillarVote{Id: projectID, Name: "Pillar2", Vote: definition.VoteYes}).Save(ctx.Storage())

		ctx.setFrontier(constants.UpdateMinNumMomentums+1, expiry+1)
		updateBlock := &nom.AccountBlock{
			Address:   types.AcceleratorContract,
			ToAddress: types.AcceleratorContract,
			Data:      definition.ABIAccelerator.PackMethodPanic(definition.UpdateMethodName),
			Amount:    big.NewInt(0),
		}
		method := &UpdateEmbeddedAcceleratorMethod{MethodName: definition.UpdateMethodName}
		if _, err := method.ReceiveBlock(ctx, updateBlock); err != nil {
			t.Fatalf("unexpected Update error: %v", err)
		}

		stored, err := definition.GetProjectEntry(ctx.Storage(), projectID)
		if err != nil {
			t.Fatalf("unexpected GetProjectEntry error: %v", err)
		}
		if stored.Status != definition.ClosedStatus {
			t.Fatalf("want status %d (ClosedStatus), got %d", definition.ClosedStatus, stored.Status)
		}
	})

	t.Run("pre-spork passing breakdown after window elapses is closed", func(t *testing.T) {
		ctx := newTestAcceleratorContext(false)
		projectID := types.HexToHashPanic("0303030303030303030303030303030303030303030303030303030303030303")
		seedProject(ctx, projectID, createTime)

		(&definition.PillarVote{Id: projectID, Name: "Pillar1", Vote: definition.VoteYes}).Save(ctx.Storage())
		(&definition.PillarVote{Id: projectID, Name: "Pillar2", Vote: definition.VoteYes}).Save(ctx.Storage())

		ctx.setFrontier(constants.UpdateMinNumMomentums+1, expiry+1)
		updateBlock := &nom.AccountBlock{
			Address:   types.AcceleratorContract,
			ToAddress: types.AcceleratorContract,
			Data:      definition.ABIAccelerator.PackMethodPanic(definition.UpdateMethodName),
			Amount:    big.NewInt(0),
		}
		method := &UpdateEmbeddedAcceleratorMethod{MethodName: definition.UpdateMethodName}
		if _, err := method.ReceiveBlock(ctx, updateBlock); err != nil {
			t.Fatalf("unexpected Update error: %v", err)
		}

		stored, err := definition.GetProjectEntry(ctx.Storage(), projectID)
		if err != nil {
			t.Fatalf("unexpected GetProjectEntry error: %v", err)
		}
		if stored.Status != definition.ClosedStatus {
			t.Fatalf("want pre-spork status %d (ClosedStatus), got %d", definition.ClosedStatus, stored.Status)
		}
	})

	t.Run("failed project write during final tally panics and never reports success", func(t *testing.T) {
		for _, passing := range []bool{true, false} {
			ctx := newTestAcceleratorContext(true)
			projectID := types.HexToHashPanic("0404040404040404040404040404040404040404040404040404040404040404")
			proj := seedProject(ctx, projectID, createTime)
			if passing {
				(&definition.PillarVote{Id: projectID, Name: "Pillar1", Vote: definition.VoteYes}).Save(ctx.Storage())
				(&definition.PillarVote{Id: projectID, Name: "Pillar2", Vote: definition.VoteYes}).Save(ctx.Storage())
			}

			writeErr := errors.New("simulated project status write failure")
			ctx.storage = &failingKeyDB{
				DB:      ctx.storage,
				failKey: proj.Key(),
				failErr: writeErr,
			}
			ctx.setFrontier(constants.UpdateMinNumMomentums+1, expiry+1)
			updateBlock := &nom.AccountBlock{
				Address:   types.AcceleratorContract,
				ToAddress: types.AcceleratorContract,
				Data:      definition.ABIAccelerator.PackMethodPanic(definition.UpdateMethodName),
				Amount:    big.NewInt(0),
			}
			method := &UpdateEmbeddedAcceleratorMethod{MethodName: definition.UpdateMethodName}

			var panicked bool
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
					}
				}()
				_, _ = method.ReceiveBlock(ctx, updateBlock)
			}()
			if !panicked {
				t.Fatalf("passing=%v: expected panic on failed project.Save during final tally", passing)
			}
		}
	})
}

func TestAccelerator_VoteProjectWindowBoundariesAndPhaseUnaffected(t *testing.T) {
	const createTime = int64(1000000200)
	expiry := createTime + constants.AcceleratorProjectVotingPeriod
	p1Addr := types.ParseAddressPanic("z1qqq43dyrswfehx9w9td43exflqzcxrt7g6alah")
	p2Addr := types.ParseAddressPanic("z1qz8v73ea2vy2rrlq7skssngu8cm8mknjjkr2ju")

	voteByNameBlock := func(addr types.Address, id types.Hash, pillar string, vote uint8) *nom.AccountBlock {
		return &nom.AccountBlock{
			Address:   addr,
			ToAddress: types.AcceleratorContract,
			Data:      definition.ABIAccelerator.PackMethodPanic(definition.VoteByNameMethodName, id, pillar, vote),
			Amount:    big.NewInt(0),
		}
	}
	voteByProdBlock := func(addr types.Address, id types.Hash, vote uint8) *nom.AccountBlock {
		return &nom.AccountBlock{
			Address:   addr,
			ToAddress: types.AcceleratorContract,
			Data:      definition.ABIAccelerator.PackMethodPanic(definition.VoteByProdAddressMethodName, id, vote),
			Amount:    big.NewInt(0),
		}
	}

	t.Run("timestamps just before, equal to, and after expiry on VoteByName and VoteByProdAddress", func(t *testing.T) {
		ctx := newTestAcceleratorContext(true)
		projectID := types.HexToHashPanic("0505050505050505050505050505050505050505050505050505050505050505")
		seedProject(ctx, projectID, createTime)

		byName := &VoteByNameMethod{MethodName: definition.VoteByNameMethodName}
		byProd := &VoteByProdAddressMethod{MethodName: definition.VoteByProdAddressMethodName}

		// Just before expiry (expiry - 1): accepted.
		ctx.setFrontier(20, expiry-1)
		if _, err := byName.ReceiveBlock(ctx, voteByNameBlock(p1Addr, projectID, "Pillar1", definition.VoteYes)); err != nil {
			t.Fatalf("expected VoteByName at expiry-1 to succeed, got %v", err)
		}
		if _, err := byProd.ReceiveBlock(ctx, voteByProdBlock(p2Addr, projectID, definition.VoteYes)); err != nil {
			t.Fatalf("expected VoteByProdAddress at expiry-1 to succeed, got %v", err)
		}

		// Equal to expiry (expiry): accepted (comparison includes equality).
		ctx.setFrontier(21, expiry)
		if _, err := byName.ReceiveBlock(ctx, voteByNameBlock(p1Addr, projectID, "Pillar1", definition.VoteAbstain)); err != nil {
			t.Fatalf("expected VoteByName at expiry to succeed, got %v", err)
		}
		if _, err := byProd.ReceiveBlock(ctx, voteByProdBlock(p2Addr, projectID, definition.VoteYes)); err != nil {
			t.Fatalf("expected VoteByProdAddress at expiry to succeed, got %v", err)
		}

		// Just after expiry (expiry + 1): rejected, including vote overwrite.
		ctx.setFrontier(22, expiry+1)
		if _, err := byName.ReceiveBlock(ctx, voteByNameBlock(p1Addr, projectID, "Pillar1", definition.VoteNo)); err != constants.ErrForbiddenParam {
			t.Fatalf("expected VoteByName at expiry+1 to fail with ErrForbiddenParam, got %v", err)
		}
		if _, err := byProd.ReceiveBlock(ctx, voteByProdBlock(p2Addr, projectID, definition.VoteNo)); err != constants.ErrForbiddenParam {
			t.Fatalf("expected VoteByProdAddress at expiry+1 to fail with ErrForbiddenParam, got %v", err)
		}

		// In-window votes stand unchanged after rejected overwrites.
		breakdown := definition.GetVoteBreakdown(ctx.Storage(), projectID)
		if breakdown.Total != 2 || breakdown.Yes != 1 || breakdown.No != 0 {
			t.Fatalf("unexpected vote breakdown after rejected overwrites: %+v", breakdown)
		}
	})

	t.Run("phase voting unaffected after project and phase windows elapse", func(t *testing.T) {
		ctx := newTestAcceleratorContext(true)
		projectID := types.HexToHashPanic("0606060606060606060606060606060606060606060606060606060606060606")
		phaseID := types.HexToHashPanic("0707070707070707070707070707070707070707070707070707070707070707")
		proj := seedProject(ctx, projectID, createTime)
		proj.Status = definition.ActiveStatus
		proj.PhaseIds = []types.Hash{phaseID}
		proj.Save(ctx.Storage())

		phase := &definition.Phase{
			Id:                phaseID,
			ProjectId:         projectID,
			Name:              "Phase 1",
			Description:       "Phase description",
			Url:               "example.com/phase1",
			ZnnFundsNeeded:    big.NewInt(10),
			QsrFundsNeeded:    big.NewInt(100),
			CreationTimestamp: createTime,
			Status:            definition.VotingStatus,
		}
		phase.Save(ctx.Storage())
		(&definition.VotableHash{Id: phaseID}).Save(ctx.Storage())

		byName := &VoteByNameMethod{MethodName: definition.VoteByNameMethodName}
		byProd := &VoteByProdAddressMethod{MethodName: definition.VoteByProdAddressMethodName}

		ctx.setFrontier(30, expiry+100000)
		if _, err := byName.ReceiveBlock(ctx, voteByNameBlock(p1Addr, phaseID, "Pillar1", definition.VoteYes)); err != nil {
			t.Fatalf("expected phase VoteByName after expiry to succeed, got %v", err)
		}
		if _, err := byProd.ReceiveBlock(ctx, voteByProdBlock(p2Addr, phaseID, definition.VoteYes)); err != nil {
			t.Fatalf("expected phase VoteByProdAddress after expiry to succeed, got %v", err)
		}
		breakdown := definition.GetVoteBreakdown(ctx.Storage(), phaseID)
		if breakdown.Total != 2 || breakdown.Yes != 2 {
			t.Fatalf("unexpected phase vote breakdown: %+v", breakdown)
		}
	})
}
