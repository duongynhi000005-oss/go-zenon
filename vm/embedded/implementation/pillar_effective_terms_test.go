package implementation

import (
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

type testPillarContext struct {
	store.Account
	momentum          *nom.Momentum
	ticker            common.Ticker
	dpEnforced        bool
	epochStats        map[uint64]*api.EpochStats
	pillarDelegations map[uint64]map[string]*types.PillarDelegationDetail
	rewards           map[uint64][]definition.RewardDeposit
}

func newTestPillarContext() *testPillarContext {
	t := time.Unix(1000, 0)
	ticker := common.NewTicker(time.Unix(0, 0), 100*time.Second) // 1 epoch = 100 seconds
	return &testPillarContext{
		Account: accountstore.NewAccountStore(types.PillarContract, db.NewMemDB()),
		momentum: &nom.Momentum{
			ChainIdentifier: 100,
			Timestamp:       &t,
			Height:          10,
		},
		ticker:            ticker,
		dpEnforced:        true,
		epochStats:        make(map[uint64]*api.EpochStats),
		pillarDelegations: make(map[uint64]map[string]*types.PillarDelegationDetail),
		rewards:           make(map[uint64][]definition.RewardDeposit),
	}
}

func (ctx *testPillarContext) setEpoch(epoch uint64) {
	// Set momentum timestamp within epoch
	startTime, _ := ctx.ticker.ToTime(epoch)
	mid := startTime.Add(10 * time.Second)
	ctx.momentum.Timestamp = &mid
}

func (ctx *testPillarContext) CacheStore() store.Cache       { return nil }
func (ctx *testPillarContext) MomentumStore() store.Momentum { return nil }

func (ctx *testPillarContext) GetFrontierMomentum() (*nom.Momentum, error) {
	return ctx.momentum, nil
}
func (ctx *testPillarContext) GetGenesisMomentum() *nom.Momentum { return ctx.momentum }

func (ctx *testPillarContext) Save()  {}
func (ctx *testPillarContext) Reset() {}
func (ctx *testPillarContext) Done()  {}

func (ctx *testPillarContext) AddBalance(ts *types.ZenonTokenStandard, amount *big.Int) {}
func (ctx *testPillarContext) SubBalance(ts *types.ZenonTokenStandard, amount *big.Int) {}

func (ctx *testPillarContext) IsAcceleratorSporkEnforced() bool        { return true }
func (ctx *testPillarContext) IsHtlcSporkEnforced() bool               { return true }
func (ctx *testPillarContext) IsBridgeAndLiquiditySporkEnforced() bool { return true }
func (ctx *testPillarContext) IsDynamicPlasmaSporkEnforced() bool      { return ctx.dpEnforced }
func (ctx *testPillarContext) IsPtlcSporkEnforced() bool               { return false }

func (ctx *testPillarContext) GetPillarWeights() (map[string]*big.Int, error) {
	return nil, nil
}
func (ctx *testPillarContext) EpochTicker() common.Ticker {
	return ctx.ticker
}
func (ctx *testPillarContext) EpochStats(epoch uint64) (*api.EpochStats, error) {
	if stats, ok := ctx.epochStats[epoch]; ok {
		return stats, nil
	}
	return &api.EpochStats{
		Epoch:       epoch,
		TotalWeight: big.NewInt(0),
		Pillars:     make(map[string]*api.EpochPillarStats),
	}, nil
}
func (ctx *testPillarContext) GetPillarDelegationsByEpoch(epoch uint64) (map[string]*types.PillarDelegationDetail, error) {
	if del, ok := ctx.pillarDelegations[epoch]; ok {
		return del, nil
	}
	return make(map[string]*types.PillarDelegationDetail), nil
}

func testUserAddress(b byte) types.Address {
	var addr types.Address
	addr[0] = types.UserAddrByte
	addr[1] = b
	addr[19] = b
	return addr
}

// TestPillarEpochTerms_StorageAndResolve tests storage, iteration order,
// and resolution logic of PillarEpochTerms.
func TestPillarEpochTerms_StorageAndResolve(t *testing.T) {
	memDB := db.NewMemDB()
	pillarName := "zenon_pillar"

	pillar := &definition.PillarInfo{
		Name:                         pillarName,
		GiveBlockRewardPercentage:    10,
		GiveDelegateRewardPercentage: 20,
	}

	// 1. Before any terms are recorded, resolution falls back to live values.
	b, d, err := definition.ResolvePillarEpochTerms(memDB, pillar, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b != 10 || d != 20 {
		t.Fatalf("expected fallback (10, 20), got (%d, %d)", b, d)
	}

	has, err := definition.HasPillarEpochTerms(memDB, pillarName)
	if err != nil || has {
		t.Fatalf("expected HasPillarEpochTerms=false, got %v, err=%v", has, err)
	}

	// 2. Save baseline at epoch 0 and updates at epoch 5 and 10.
	terms0 := &definition.PillarEpochTerms{
		Name:                         pillarName,
		EffectiveEpoch:               0,
		GiveBlockRewardPercentage:    10,
		GiveDelegateRewardPercentage: 20,
	}
	if err := terms0.Save(memDB); err != nil {
		t.Fatalf("save terms0 error: %v", err)
	}

	terms5 := &definition.PillarEpochTerms{
		Name:                         pillarName,
		EffectiveEpoch:               5,
		GiveBlockRewardPercentage:    30,
		GiveDelegateRewardPercentage: 40,
	}
	if err := terms5.Save(memDB); err != nil {
		t.Fatalf("save terms5 error: %v", err)
	}

	terms10 := &definition.PillarEpochTerms{
		Name:                         pillarName,
		EffectiveEpoch:               10,
		GiveBlockRewardPercentage:    50,
		GiveDelegateRewardPercentage: 60,
	}
	if err := terms10.Save(memDB); err != nil {
		t.Fatalf("save terms10 error: %v", err)
	}

	has, err = definition.HasPillarEpochTerms(memDB, pillarName)
	if err != nil || !has {
		t.Fatalf("expected HasPillarEpochTerms=true, got %v, err=%v", has, err)
	}

	// Test Resolve at different epochs:
	// Epoch 0, 1, 4 -> terms0 (10, 20)
	for _, epoch := range []uint64{0, 1, 4} {
		b, d, err = definition.ResolvePillarEpochTerms(memDB, pillar, epoch)
		if err != nil {
			t.Fatalf("epoch %d: unexpected err %v", epoch, err)
		}
		if b != 10 || d != 20 {
			t.Fatalf("epoch %d: expected (10, 20), got (%d, %d)", epoch, b, d)
		}
	}

	// Epoch 5, 6, 9 -> terms5 (30, 40)
	for _, epoch := range []uint64{5, 6, 9} {
		b, d, err = definition.ResolvePillarEpochTerms(memDB, pillar, epoch)
		if err != nil {
			t.Fatalf("epoch %d: unexpected err %v", epoch, err)
		}
		if b != 30 || d != 40 {
			t.Fatalf("epoch %d: expected (30, 40), got (%d, %d)", epoch, b, d)
		}
	}

	// Epoch 10, 11, 100 -> terms10 (50, 60)
	for _, epoch := range []uint64{10, 11, 100} {
		b, d, err = definition.ResolvePillarEpochTerms(memDB, pillar, epoch)
		if err != nil {
			t.Fatalf("epoch %d: unexpected err %v", epoch, err)
		}
		if b != 50 || d != 60 {
			t.Fatalf("epoch %d: expected (50, 60), got (%d, %d)", epoch, b, d)
		}
	}

	// Test GetPillarEpochTermsList ordering
	list, err := definition.GetPillarEpochTermsList(memDB, pillarName)
	if err != nil {
		t.Fatalf("GetPillarEpochTermsList error: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(list))
	}
	if list[0].EffectiveEpoch != 0 || list[1].EffectiveEpoch != 5 || list[2].EffectiveEpoch != 10 {
		t.Fatalf("unexpected list ordering: %v, %v, %v", list[0].EffectiveEpoch, list[1].EffectiveEpoch, list[2].EffectiveEpoch)
	}
}

// TestPillar_UpdatePillarMethod_ReceiveBlock verifies that UpdatePillarMethod.ReceiveBlock
// under DynamicPlasmaSpork records effective terms starting at currentEpoch + 1.
func TestPillar_UpdatePillarMethod_ReceiveBlock(t *testing.T) {
	ctx := newTestPillarContext()
	method := &UpdatePillarMethod{MethodName: definition.UpdatePillarMethodName}

	stakeAddr := testUserAddress(1)
	producerAddr := testUserAddress(2)
	rewardAddr := testUserAddress(3)
	pillarName := "test_pillar"

	// Save initial pillar in storage
	initPillar := &definition.PillarInfo{
		Name:                         pillarName,
		BlockProducingAddress:        producerAddr,
		RewardWithdrawAddress:        rewardAddr,
		StakeAddress:                 stakeAddr,
		Amount:                       constants.PillarStakeAmount,
		RegistrationTime:             1000,
		RevokeTime:                   0,
		GiveBlockRewardPercentage:    50,
		GiveDelegateRewardPercentage: 50,
		PillarType:                   definition.NormalPillarType,
	}
	if err := initPillar.Save(ctx.Storage()); err != nil {
		t.Fatalf("save pillar error: %v", err)
	}

	// 1. Before DynamicPlasmaSpork: update in epoch 2 does NOT write effective terms.
	ctx.dpEnforced = false
	ctx.setEpoch(2)

	sendBlock := &nom.AccountBlock{
		Address:   stakeAddr,
		ToAddress: types.PillarContract,
		Amount:    big.NewInt(0),
		Data: definition.ABIPillars.PackMethodPanic(
			definition.UpdatePillarMethodName,
			pillarName,
			producerAddr,
			rewardAddr,
			uint8(20),
			uint8(20),
		),
	}

	_, err := method.ReceiveBlock(ctx, sendBlock)
	if err != nil {
		t.Fatalf("update before spork failed: %v", err)
	}

	has, err := definition.HasPillarEpochTerms(ctx.Storage(), pillarName)
	if err != nil || has {
		t.Fatalf("expected HasPillarEpochTerms=false before spork, got %v", has)
	}

	savedPillar, err := definition.GetPillarInfo(ctx.Storage(), pillarName)
	if err != nil {
		t.Fatalf("get pillar error: %v", err)
	}
	if savedPillar.GiveBlockRewardPercentage != 20 || savedPillar.GiveDelegateRewardPercentage != 20 {
		t.Fatalf("expected live values updated to 20/20, got %d/%d", savedPillar.GiveBlockRewardPercentage, savedPillar.GiveDelegateRewardPercentage)
	}

	// 2. Activate DynamicPlasmaSpork: update in epoch 4 to 80/10
	ctx.dpEnforced = true
	ctx.setEpoch(4)

	sendBlock.Data = definition.ABIPillars.PackMethodPanic(
		definition.UpdatePillarMethodName,
		pillarName,
		producerAddr,
		rewardAddr,
		uint8(80),
		uint8(10),
	)

	_, err = method.ReceiveBlock(ctx, sendBlock)
	if err != nil {
		t.Fatalf("update after spork failed: %v", err)
	}

	// Baseline entry at 0 should be 20/20, new entry at 5 should be 80/10.
	terms0, err := definition.GetPillarEpochTerms(ctx.Storage(), pillarName, 0)
	if err != nil {
		t.Fatalf("expected baseline terms at 0, got err: %v", err)
	}
	if terms0.GiveBlockRewardPercentage != 20 || terms0.GiveDelegateRewardPercentage != 20 {
		t.Fatalf("expected baseline (20, 20), got (%d, %d)", terms0.GiveBlockRewardPercentage, terms0.GiveDelegateRewardPercentage)
	}

	terms5, err := definition.GetPillarEpochTerms(ctx.Storage(), pillarName, 5)
	if err != nil {
		t.Fatalf("expected new terms at 5, got err: %v", err)
	}
	if terms5.GiveBlockRewardPercentage != 80 || terms5.GiveDelegateRewardPercentage != 10 {
		t.Fatalf("expected new terms (80, 10), got (%d, %d)", terms5.GiveBlockRewardPercentage, terms5.GiveDelegateRewardPercentage)
	}

	// 3. Second update within the same epoch 4 to 90/5
	sendBlock.Data = definition.ABIPillars.PackMethodPanic(
		definition.UpdatePillarMethodName,
		pillarName,
		producerAddr,
		rewardAddr,
		uint8(90),
		uint8(5),
	)
	_, err = method.ReceiveBlock(ctx, sendBlock)
	if err != nil {
		t.Fatalf("second update in epoch 4 failed: %v", err)
	}

	// Entry at 5 should be updated to 90/5; baseline at 0 unchanged.
	terms0, err = definition.GetPillarEpochTerms(ctx.Storage(), pillarName, 0)
	if err != nil || terms0.GiveBlockRewardPercentage != 20 || terms0.GiveDelegateRewardPercentage != 20 {
		t.Fatalf("baseline corrupted: %v, (%d, %d)", err, terms0.GiveBlockRewardPercentage, terms0.GiveDelegateRewardPercentage)
	}
	terms5, err = definition.GetPillarEpochTerms(ctx.Storage(), pillarName, 5)
	if err != nil || terms5.GiveBlockRewardPercentage != 90 || terms5.GiveDelegateRewardPercentage != 5 {
		t.Fatalf("expected terms at 5 updated to (90, 5), got (%d, %d)", terms5.GiveBlockRewardPercentage, terms5.GiveDelegateRewardPercentage)
	}

	// 4. Update in epoch 6: changing back to original values 20/20
	ctx.setEpoch(6)
	sendBlock.Data = definition.ABIPillars.PackMethodPanic(
		definition.UpdatePillarMethodName,
		pillarName,
		producerAddr,
		rewardAddr,
		uint8(20),
		uint8(20),
	)
	_, err = method.ReceiveBlock(ctx, sendBlock)
	if err != nil {
		t.Fatalf("update in epoch 6 failed: %v", err)
	}

	terms7, err := definition.GetPillarEpochTerms(ctx.Storage(), pillarName, 7)
	if err != nil || terms7.GiveBlockRewardPercentage != 20 || terms7.GiveDelegateRewardPercentage != 20 {
		t.Fatalf("expected terms at 7 to be (20, 20), got %v, (%d, %d)", err, terms7.GiveBlockRewardPercentage, terms7.GiveDelegateRewardPercentage)
	}

	// 5. Updating only reward address in epoch 8 (percentages unchanged)
	newRewardAddr := testUserAddress(4)
	ctx.setEpoch(8)
	sendBlock.Data = definition.ABIPillars.PackMethodPanic(
		definition.UpdatePillarMethodName,
		pillarName,
		producerAddr,
		newRewardAddr,
		uint8(20),
		uint8(20),
	)
	_, err = method.ReceiveBlock(ctx, sendBlock)
	if err != nil {
		t.Fatalf("reward address update failed: %v", err)
	}

	// No terms entry should exist for epoch 9
	_, err = definition.GetPillarEpochTerms(ctx.Storage(), pillarName, 9)
	if err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent for epoch 9, got %v", err)
	}
}

// TestPillar_RewardSplitAcrossEpochs exercises the entire settlement workflow
// verifying the acceptance requirements of issue #228:
// - A percentage change applied after epoch ends and before update does not affect that epoch's split.
// - Multiple unsettled epochs are unaffected.
// - Changes apply from epoch + 1.
// - Unchanged pillars settle identically before and after.
func TestPillar_RewardSplitAcrossEpochs(t *testing.T) {
	ctx := newTestPillarContext()
	method := &UpdatePillarMethod{MethodName: definition.UpdatePillarMethodName}

	stakeP1 := testUserAddress(10)
	stakeP2 := testUserAddress(11)
	backer1 := testUserAddress(12)

	p1 := &definition.PillarInfo{
		Name:                         "pillar_one",
		BlockProducingAddress:        stakeP1,
		RewardWithdrawAddress:        stakeP1,
		StakeAddress:                 stakeP1,
		Amount:                       constants.PillarStakeAmount,
		RegistrationTime:             100,
		GiveBlockRewardPercentage:    0,
		GiveDelegateRewardPercentage: 100, // 0/100 initially
		PillarType:                   definition.NormalPillarType,
	}
	p2 := &definition.PillarInfo{
		Name:                         "pillar_two_unchanged",
		BlockProducingAddress:        stakeP2,
		RewardWithdrawAddress:        stakeP2,
		StakeAddress:                 stakeP2,
		Amount:                       constants.PillarStakeAmount,
		RegistrationTime:             100,
		GiveBlockRewardPercentage:    50,
		GiveDelegateRewardPercentage: 50, // 50/50 throughout
		PillarType:                   definition.NormalPillarType,
	}
	if err := p1.Save(ctx.Storage()); err != nil {
		t.Fatal(err)
	}
	if err := p2.Save(ctx.Storage()); err != nil {
		t.Fatal(err)
	}

	// Setup delegations: backer1 delegates 1000 weight to pillar_one
	delegations := map[string]*types.PillarDelegationDetail{
		"pillar_one": {
			PillarDelegation: types.PillarDelegation{Name: "pillar_one"},
			Backers: map[types.Address]*big.Int{
				backer1: big.NewInt(1000),
			},
		},
		"pillar_two_unchanged": {
			PillarDelegation: types.PillarDelegation{Name: "pillar_two_unchanged"},
			Backers: map[types.Address]*big.Int{
				backer1: big.NewInt(1000),
			},
		},
	}
	ctx.pillarDelegations[1] = delegations
	ctx.pillarDelegations[2] = delegations
	ctx.pillarDelegations[3] = delegations

	// Setup stats for epochs 1, 2, 3: each pillar produces blocks and has weight
	setupStats := func(epoch uint64) {
		ctx.epochStats[epoch] = &api.EpochStats{
			Epoch:       epoch,
			TotalWeight: big.NewInt(2000),
			Pillars: map[string]*api.EpochPillarStats{
				"pillar_one": {
					Name:             "pillar_one",
					BlockNum:         10,
					ExpectedBlockNum: 10,
					Weight:           big.NewInt(1000),
				},
				"pillar_two_unchanged": {
					Name:             "pillar_two_unchanged",
					BlockNum:         10,
					ExpectedBlockNum: 10,
					Weight:           big.NewInt(1000),
				},
			},
		}
	}
	setupStats(1)
	setupStats(2)
	setupStats(3)

	// In epoch 2 (frontier):
	// Pillar 1 updates give-percentages from 0/100 to 0/0 (flipping delegator share to 0).
	ctx.setEpoch(2)
	sendBlock := &nom.AccountBlock{
		Address:   stakeP1,
		ToAddress: types.PillarContract,
		Amount:    big.NewInt(0),
		Data: definition.ABIPillars.PackMethodPanic(
			definition.UpdatePillarMethodName,
			"pillar_one",
			stakeP1,
			stakeP1,
			uint8(0),
			uint8(0), // Flipped to 0/0
		),
	}
	if _, err := method.ReceiveBlock(ctx, sendBlock); err != nil {
		t.Fatalf("ReceiveBlock failed: %v", err)
	}

	// Now computeDetailedPillarReward for epoch 1 (which completed earlier and is now being settled in epoch 2)
	if err := computeDetailedPillarReward(ctx, 1); err != nil {
		t.Fatalf("computeDetailedPillarReward epoch 1 failed: %v", err)
	}

	// Check PillarEpochHistory for epoch 1:
	// Pillar 1 MUST have 0% block, 100% delegate (the terms in force during epoch 1)!
	histP1Epoch1, err := definition.GetPillarEpochHistoryList(ctx.Storage(), 1)
	if err != nil {
		t.Fatal(err)
	}
	foundP1 := false
	for _, h := range histP1Epoch1 {
		if h.Name == "pillar_one" {
			foundP1 = true
			if h.GiveBlockRewardPercentage != 0 || h.GiveDelegateRewardPercentage != 100 {
				t.Fatalf("epoch 1 history for pillar_one expected 0/100, got %d/%d", h.GiveBlockRewardPercentage, h.GiveDelegateRewardPercentage)
			}
		}
		if h.Name == "pillar_two_unchanged" {
			if h.GiveBlockRewardPercentage != 50 || h.GiveDelegateRewardPercentage != 50 {
				t.Fatalf("epoch 1 history for unchanged pillar expected 50/50, got %d/%d", h.GiveBlockRewardPercentage, h.GiveDelegateRewardPercentage)
			}
		}
	}
	if !foundP1 {
		t.Fatal("pillar_one history not found for epoch 1")
	}

	// Now settle epoch 2 (the epoch during which the update occurred):
	// Since update happened during epoch 2, it takes effect from epoch 3!
	// So epoch 2 MUST ALSO be settled under 0/100!
	if err := computeDetailedPillarReward(ctx, 2); err != nil {
		t.Fatalf("computeDetailedPillarReward epoch 2 failed: %v", err)
	}

	histP1Epoch2, err := definition.GetPillarEpochHistoryList(ctx.Storage(), 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range histP1Epoch2 {
		if h.Name == "pillar_one" {
			if h.GiveBlockRewardPercentage != 0 || h.GiveDelegateRewardPercentage != 100 {
				t.Fatalf("epoch 2 history for pillar_one expected 0/100, got %d/%d", h.GiveBlockRewardPercentage, h.GiveDelegateRewardPercentage)
			}
		}
	}

	// Now settle epoch 3 (the epoch where 0/0 takes effect):
	// Pillar 1 should be settled under 0/0!
	if err := computeDetailedPillarReward(ctx, 3); err != nil {
		t.Fatalf("computeDetailedPillarReward epoch 3 failed: %v", err)
	}

	histP1Epoch3, err := definition.GetPillarEpochHistoryList(ctx.Storage(), 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range histP1Epoch3 {
		if h.Name == "pillar_one" {
			if h.GiveBlockRewardPercentage != 0 || h.GiveDelegateRewardPercentage != 0 {
				t.Fatalf("epoch 3 history for pillar_one expected 0/0, got %d/%d", h.GiveBlockRewardPercentage, h.GiveDelegateRewardPercentage)
			}
		}
		if h.Name == "pillar_two_unchanged" {
			if h.GiveBlockRewardPercentage != 50 || h.GiveDelegateRewardPercentage != 50 {
				t.Fatalf("epoch 3 history for unchanged pillar expected 50/50, got %d/%d", h.GiveBlockRewardPercentage, h.GiveDelegateRewardPercentage)
			}
		}
	}
}

// TestPillar_SeveralUnsettledEpochsUnaffected tests that with several epochs
// (e.g. 5, 6, 7) unsettled, a change made in epoch 7 does not affect any of them.
func TestPillar_SeveralUnsettledEpochsUnaffected(t *testing.T) {
	ctx := newTestPillarContext()
	method := &UpdatePillarMethod{MethodName: definition.UpdatePillarMethodName}

	stakeP1 := testUserAddress(20)
	backer := testUserAddress(21)

	p1 := &definition.PillarInfo{
		Name:                         "multi_epoch_pillar",
		BlockProducingAddress:        stakeP1,
		RewardWithdrawAddress:        stakeP1,
		StakeAddress:                 stakeP1,
		Amount:                       constants.PillarStakeAmount,
		RegistrationTime:             100,
		GiveBlockRewardPercentage:    40,
		GiveDelegateRewardPercentage: 60,
		PillarType:                   definition.NormalPillarType,
	}
	if err := p1.Save(ctx.Storage()); err != nil {
		t.Fatal(err)
	}

	for ep := uint64(5); ep <= 8; ep++ {
		ctx.pillarDelegations[ep] = map[string]*types.PillarDelegationDetail{
			"multi_epoch_pillar": {
				PillarDelegation: types.PillarDelegation{Name: "multi_epoch_pillar"},
				Backers: map[types.Address]*big.Int{
					backer: big.NewInt(1000),
				},
			},
		}
		ctx.epochStats[ep] = &api.EpochStats{
			Epoch:       ep,
			TotalWeight: big.NewInt(1000),
			Pillars: map[string]*api.EpochPillarStats{
				"multi_epoch_pillar": {
					Name:             "multi_epoch_pillar",
					BlockNum:         10,
					ExpectedBlockNum: 10,
					Weight:           big.NewInt(1000),
				},
			},
		}
	}

	// In epoch 7: change give percentages to 5/5
	ctx.setEpoch(7)
	sendBlock := &nom.AccountBlock{
		Address:   stakeP1,
		ToAddress: types.PillarContract,
		Amount:    big.NewInt(0),
		Data: definition.ABIPillars.PackMethodPanic(
			definition.UpdatePillarMethodName,
			"multi_epoch_pillar",
			stakeP1,
			stakeP1,
			uint8(5),
			uint8(5),
		),
	}
	if _, err := method.ReceiveBlock(ctx, sendBlock); err != nil {
		t.Fatalf("ReceiveBlock failed: %v", err)
	}

	// Settle unsettled epochs 5, 6, 7
	for _, ep := range []uint64{5, 6, 7} {
		if err := computeDetailedPillarReward(ctx, ep); err != nil {
			t.Fatalf("computeDetailedPillarReward epoch %d failed: %v", ep, err)
		}
		histList, err := definition.GetPillarEpochHistoryList(ctx.Storage(), ep)
		if err != nil {
			t.Fatal(err)
		}
		if len(histList) == 0 {
			t.Fatalf("epoch %d: expected history, got 0", ep)
		}
		h := histList[0]
		if h.GiveBlockRewardPercentage != 40 || h.GiveDelegateRewardPercentage != 60 {
			t.Fatalf("epoch %d: expected 40/60, got %d/%d", ep, h.GiveBlockRewardPercentage, h.GiveDelegateRewardPercentage)
		}
	}

	// Now settle epoch 8: terms 5/5 are effective
	if err := computeDetailedPillarReward(ctx, 8); err != nil {
		t.Fatalf("computeDetailedPillarReward epoch 8 failed: %v", err)
	}
	histList, err := definition.GetPillarEpochHistoryList(ctx.Storage(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if histList[0].GiveBlockRewardPercentage != 5 || histList[0].GiveDelegateRewardPercentage != 5 {
		t.Fatalf("epoch 8: expected 5/5, got %d/%d", histList[0].GiveBlockRewardPercentage, histList[0].GiveDelegateRewardPercentage)
	}
}

// TestPillar_ActivationTransition tests that:
// 1. A change executed before activation followed by settlement after activation
//    settles with live values (as today).
// 2. Epochs settled before activation are untouched.
// 3. Subsequent post-activation updates preserve unsettled terms correctly.
func TestPillar_ActivationTransition(t *testing.T) {
	ctx := newTestPillarContext()
	method := &UpdatePillarMethod{MethodName: definition.UpdatePillarMethodName}

	stakeP1 := testUserAddress(30)
	backer := testUserAddress(31)

	p1 := &definition.PillarInfo{
		Name:                         "transition_pillar",
		BlockProducingAddress:        stakeP1,
		RewardWithdrawAddress:        stakeP1,
		StakeAddress:                 stakeP1,
		Amount:                       constants.PillarStakeAmount,
		RegistrationTime:             100,
		GiveBlockRewardPercentage:    10,
		GiveDelegateRewardPercentage: 90,
		PillarType:                   definition.NormalPillarType,
	}
	if err := p1.Save(ctx.Storage()); err != nil {
		t.Fatal(err)
	}

	for ep := uint64(1); ep <= 4; ep++ {
		ctx.pillarDelegations[ep] = map[string]*types.PillarDelegationDetail{
			"transition_pillar": {
				PillarDelegation: types.PillarDelegation{Name: "transition_pillar"},
				Backers: map[types.Address]*big.Int{
					backer: big.NewInt(1000),
				},
			},
		}
		ctx.epochStats[ep] = &api.EpochStats{
			Epoch:       ep,
			TotalWeight: big.NewInt(1000),
			Pillars: map[string]*api.EpochPillarStats{
				"transition_pillar": {
					Name:             "transition_pillar",
					BlockNum:         10,
					ExpectedBlockNum: 10,
					Weight:           big.NewInt(1000),
				},
			},
		}
	}

	// 1. Settle epoch 1 before activation:
	ctx.dpEnforced = false
	if err := computeDetailedPillarReward(ctx, 1); err != nil {
		t.Fatal(err)
	}
	hist1, err := definition.GetPillarEpochHistoryList(ctx.Storage(), 1)
	if err != nil || hist1[0].GiveBlockRewardPercentage != 10 || hist1[0].GiveDelegateRewardPercentage != 90 {
		t.Fatalf("epoch 1 pre-activation error: %v", err)
	}

	// 2. Before activation, update in epoch 2 to 25/75 (without spork)
	ctx.setEpoch(2)
	sendBlock := &nom.AccountBlock{
		Address:   stakeP1,
		ToAddress: types.PillarContract,
		Amount:    big.NewInt(0),
		Data: definition.ABIPillars.PackMethodPanic(
			definition.UpdatePillarMethodName,
			"transition_pillar",
			stakeP1,
			stakeP1,
			uint8(25),
			uint8(75),
		),
	}
	if _, err := method.ReceiveBlock(ctx, sendBlock); err != nil {
		t.Fatal(err)
	}

	// 3. Now activate DynamicPlasmaSpork at epoch 3
	ctx.dpEnforced = true
	ctx.setEpoch(3)

	// Settle epoch 2 after activation: since change was executed before activation,
	// no effective terms entry was recorded in prefix 6. Transition rule: settled
	// with live values as today (25/75).
	if err := computeDetailedPillarReward(ctx, 2); err != nil {
		t.Fatal(err)
	}
	hist2, err := definition.GetPillarEpochHistoryList(ctx.Storage(), 2)
	if err != nil || hist2[0].GiveBlockRewardPercentage != 25 || hist2[0].GiveDelegateRewardPercentage != 75 {
		t.Fatalf("epoch 2 transition settlement expected 25/75, got %v", hist2)
	}

	// Pre-activation settled epoch 1 is untouched:
	hist1Check, err := definition.GetPillarEpochHistoryList(ctx.Storage(), 1)
	if err != nil || hist1Check[0].GiveBlockRewardPercentage != 10 || hist1Check[0].GiveDelegateRewardPercentage != 90 {
		t.Fatalf("epoch 1 pre-activation settlement altered: %v", hist1Check)
	}

	// 4. Now execute a new update AFTER activation during epoch 3 to 70/30:
	sendBlock.Data = definition.ABIPillars.PackMethodPanic(
		definition.UpdatePillarMethodName,
		"transition_pillar",
		stakeP1,
		stakeP1,
		uint8(70),
		uint8(30),
	)
	if _, err := method.ReceiveBlock(ctx, sendBlock); err != nil {
		t.Fatal(err)
	}

	// Settle epoch 3: should settle with 25/75 (since update in epoch 3 takes effect from epoch 4)
	if err := computeDetailedPillarReward(ctx, 3); err != nil {
		t.Fatal(err)
	}
	hist3, err := definition.GetPillarEpochHistoryList(ctx.Storage(), 3)
	if err != nil || hist3[0].GiveBlockRewardPercentage != 25 || hist3[0].GiveDelegateRewardPercentage != 75 {
		t.Fatalf("epoch 3 expected 25/75, got %d/%d", hist3[0].GiveBlockRewardPercentage, hist3[0].GiveDelegateRewardPercentage)
	}

	// Settle epoch 4: should settle with 70/30
	if err := computeDetailedPillarReward(ctx, 4); err != nil {
		t.Fatal(err)
	}
	hist4, err := definition.GetPillarEpochHistoryList(ctx.Storage(), 4)
	if err != nil || hist4[0].GiveBlockRewardPercentage != 70 || hist4[0].GiveDelegateRewardPercentage != 30 {
		t.Fatalf("epoch 4 expected 70/30, got %d/%d", hist4[0].GiveBlockRewardPercentage, hist4[0].GiveDelegateRewardPercentage)
	}
}

