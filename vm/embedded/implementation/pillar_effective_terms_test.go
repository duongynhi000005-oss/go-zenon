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

type testPillarContext struct {
	store.Account
	accountStoreSnapshot store.Account
	momentum             *nom.Momentum
	ticker               common.Ticker
	dpEnforced           bool
	epochStats           map[uint64]*api.EpochStats
	pillarDelegations    map[uint64]map[string]*types.PillarDelegationDetail
	rewards              map[uint64][]definition.RewardDeposit
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

func (ctx *testPillarContext) Save() {
	s := ctx.Account.Snapshot()
	ctx.accountStoreSnapshot = ctx.Account
	ctx.Account = s
}
func (ctx *testPillarContext) Reset() {
	ctx.Account = ctx.accountStoreSnapshot
	ctx.accountStoreSnapshot = nil
}
func (ctx *testPillarContext) Done() {
	changes, _ := ctx.Account.Changes()
	ctx.Account = ctx.accountStoreSnapshot
	ctx.accountStoreSnapshot = nil
	common.DealWithErr(ctx.Account.Apply(changes))
}

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

func assertEpochRewardHistory(t *testing.T, storage db.DB, epoch uint64, addr types.Address, wantZnn int64) *big.Int {
	t.Helper()
	hist, err := definition.GetRewardDepositHistory(storage, epoch, &addr)
	if err != nil {
		t.Fatalf("epoch %d GetRewardDepositHistory(%v) error: %v", epoch, addr, err)
	}
	want := big.NewInt(wantZnn)
	if hist.Znn.Cmp(want) != 0 || hist.Qsr.Sign() != 0 {
		t.Fatalf("epoch %d reward history for %v: want znn=%v qsr=0, got znn=%v qsr=%v", epoch, addr, want, hist.Znn, hist.Qsr)
	}
	return new(big.Int).Set(hist.Znn)
}

func assertCumulativeRewardDeposit(t *testing.T, storage db.DB, addr types.Address, wantZnn int64) *big.Int {
	t.Helper()
	dep, err := definition.GetRewardDeposit(storage, &addr)
	if err != nil {
		t.Fatalf("GetRewardDeposit(%v) error: %v", addr, err)
	}
	want := big.NewInt(wantZnn)
	if dep.Znn.Cmp(want) != 0 || dep.Qsr.Sign() != 0 {
		t.Fatalf("cumulative RewardDeposit for %v: want znn=%v qsr=0, got znn=%v qsr=%v", addr, want, dep.Znn, dep.Qsr)
	}
	return new(big.Int).Set(dep.Znn)
}

// TestPillarEpochTerms_StorageAndResolve tests storage, big-endian iteration order
// across the 255/256 and 65535/65536 multi-byte epoch boundaries, and resolution logic.
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

	// 2. Save baseline at epoch 0 and updates across single-byte (255 -> 256)
	// and two-byte (65535 -> 65536) big-endian boundaries.
	fixtures := []definition.PillarEpochTerms{
		{Name: pillarName, EffectiveEpoch: 0, GiveBlockRewardPercentage: 10, GiveDelegateRewardPercentage: 20},
		{Name: pillarName, EffectiveEpoch: 5, GiveBlockRewardPercentage: 30, GiveDelegateRewardPercentage: 40},
		{Name: pillarName, EffectiveEpoch: 10, GiveBlockRewardPercentage: 50, GiveDelegateRewardPercentage: 60},
		{Name: pillarName, EffectiveEpoch: 255, GiveBlockRewardPercentage: 55, GiveDelegateRewardPercentage: 65},
		{Name: pillarName, EffectiveEpoch: 256, GiveBlockRewardPercentage: 70, GiveDelegateRewardPercentage: 80},
		{Name: pillarName, EffectiveEpoch: 65535, GiveBlockRewardPercentage: 75, GiveDelegateRewardPercentage: 85},
		{Name: pillarName, EffectiveEpoch: 65536, GiveBlockRewardPercentage: 90, GiveDelegateRewardPercentage: 95},
	}
	// Insert out of order (256 before 255, 65536 before 65535) to prove key ordering is driven by big-endian bytes.
	insertOrder := []int{0, 1, 2, 4, 3, 6, 5}
	for _, idx := range insertOrder {
		entry := fixtures[idx]
		if err := entry.Save(memDB); err != nil {
			t.Fatalf("save terms at epoch %d error: %v", entry.EffectiveEpoch, err)
		}
	}

	has, err = definition.HasPillarEpochTerms(memDB, pillarName)
	if err != nil || !has {
		t.Fatalf("expected HasPillarEpochTerms=true, got %v, err=%v", has, err)
	}

	// Verify ResolvePillarEpochTerms across all boundaries including 254, 255, 256, 257 and 65535, 65536:
	cases := []struct {
		epochs    []uint64
		wantBlock uint8
		wantDel   uint8
	}{
		{[]uint64{0, 1, 4}, 10, 20},
		{[]uint64{5, 6, 9}, 30, 40},
		{[]uint64{10, 11, 100, 254}, 50, 60},
		{[]uint64{255}, 55, 65},
		{[]uint64{256, 257, 1000, 65534}, 70, 80},
		{[]uint64{65535}, 75, 85},
		{[]uint64{65536, 65537, 100000}, 90, 95},
	}
	for _, tc := range cases {
		for _, epoch := range tc.epochs {
			b, d, err = definition.ResolvePillarEpochTerms(memDB, pillar, epoch)
			if err != nil {
				t.Fatalf("epoch %d: unexpected err %v", epoch, err)
			}
			if b != tc.wantBlock || d != tc.wantDel {
				t.Fatalf("epoch %d: expected (%d, %d), got (%d, %d)", epoch, tc.wantBlock, tc.wantDel, b, d)
			}
		}
	}

	// Verify GetPillarEpochTermsList ascending big-endian order across 255/256 and 65535/65536
	list, err := definition.GetPillarEpochTermsList(memDB, pillarName)
	if err != nil {
		t.Fatalf("GetPillarEpochTermsList error: %v", err)
	}
	if len(list) != len(fixtures) {
		t.Fatalf("expected %d entries, got %d", len(fixtures), len(list))
	}
	for i, want := range fixtures {
		if list[i].EffectiveEpoch != want.EffectiveEpoch ||
			list[i].GiveBlockRewardPercentage != want.GiveBlockRewardPercentage ||
			list[i].GiveDelegateRewardPercentage != want.GiveDelegateRewardPercentage {
			t.Fatalf("entry %d: expected %+v, got %+v", i, want, *list[i])
		}
	}
}

// TestPillar_UpdatePillarMethod_ReceiveBlock verifies that UpdatePillarMethod.ReceiveBlock
// under DynamicPlasmaSpork records effective terms starting at currentEpoch + 1,
// including multiple updates in one epoch, reverting to original values, and updating
// each percentage independently.
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

	// 6. Updating only GiveBlockRewardPercentage in epoch 10 (20/20 -> 60/20)
	ctx.setEpoch(10)
	sendBlock.Data = definition.ABIPillars.PackMethodPanic(
		definition.UpdatePillarMethodName,
		pillarName,
		producerAddr,
		newRewardAddr,
		uint8(60),
		uint8(20),
	)
	if _, err := method.ReceiveBlock(ctx, sendBlock); err != nil {
		t.Fatalf("block-only percentage update failed: %v", err)
	}
	terms11, err := definition.GetPillarEpochTerms(ctx.Storage(), pillarName, 11)
	if err != nil || terms11.GiveBlockRewardPercentage != 60 || terms11.GiveDelegateRewardPercentage != 20 {
		t.Fatalf("expected terms at 11 to be (60, 20), got %v, (%d, %d)", err, terms11.GiveBlockRewardPercentage, terms11.GiveDelegateRewardPercentage)
	}

	// 7. Updating only GiveDelegateRewardPercentage in epoch 11 (60/20 -> 60/75)
	ctx.setEpoch(11)
	sendBlock.Data = definition.ABIPillars.PackMethodPanic(
		definition.UpdatePillarMethodName,
		pillarName,
		producerAddr,
		newRewardAddr,
		uint8(60),
		uint8(75),
	)
	if _, err := method.ReceiveBlock(ctx, sendBlock); err != nil {
		t.Fatalf("delegate-only percentage update failed: %v", err)
	}
	terms12, err := definition.GetPillarEpochTerms(ctx.Storage(), pillarName, 12)
	if err != nil || terms12.GiveBlockRewardPercentage != 60 || terms12.GiveDelegateRewardPercentage != 75 {
		t.Fatalf("expected terms at 12 to be (60, 75), got %v, (%d, %d)", err, terms12.GiveBlockRewardPercentage, terms12.GiveDelegateRewardPercentage)
	}
}

// TestPillar_RewardSplitAcrossEpochs exercises the entire settlement workflow
// verifying the acceptance requirements of issue #228 and pinning exact
// RewardDeposit and RewardDepositHistory amounts for delegators, pillars, and their total:
// - A percentage change applied after epoch ends and before update does not affect that epoch's split.
// - Changes apply from epoch + 1 (including independent block-only and delegate-only updates).
// - Unchanged pillars settle identically before and after.
func TestPillar_RewardSplitAcrossEpochs(t *testing.T) {
	ctx := newTestPillarContext()
	method := &UpdatePillarMethod{MethodName: definition.UpdatePillarMethodName}

	stakeP1 := testUserAddress(10)
	stakeP2 := testUserAddress(11)
	backer1 := testUserAddress(12)
	backer2 := testUserAddress(13)
	controlBacker := testUserAddress(14)

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

	// Delegations:
	// - pillar_one has backer1 (weight 600) and backer2 (weight 400), total 1000.
	// - pillar_two_unchanged has controlBacker (weight 1000).
	delegations := map[string]*types.PillarDelegationDetail{
		"pillar_one": {
			PillarDelegation: types.PillarDelegation{Name: "pillar_one"},
			Backers: map[types.Address]*big.Int{
				backer1: big.NewInt(600),
				backer2: big.NewInt(400),
			},
		},
		"pillar_two_unchanged": {
			PillarDelegation: types.PillarDelegation{Name: "pillar_two_unchanged"},
			Backers: map[types.Address]*big.Int{
				controlBacker: big.NewInt(1000),
			},
		},
	}

	// Setup stats for epochs 1..5: each pillar produces 10/10 momentums and has weight 1000/2000.
	// Per-epoch raw rewards for each pillar (tick 0, epochs < 30):
	//   BlockReward      = 83,333,333 * 10 = 833,333,330
	//   DelegationReward = 40,000,000 * 10 * 1000 * 20 / 10 / 2000 = 400,000,000
	//   TotalReward      = 1,233,333,330
	const (
		pillarTotalReward    int64 = 1233333330
		p1Epoch12PillarZnn   int64 = 833333330  // 100% of BlockReward (833,333,330) + 0% of DelegationReward
		p1Epoch12Backer1Znn  int64 = 240000000  // 60% of 100% DelegationReward (400,000,000)
		p1Epoch12Backer2Znn  int64 = 160000000  // 40% of 100% DelegationReward (400,000,000)
		p1Epoch3PillarZnn    int64 = 1233333330 // 0/0 split: 100% of TotalReward goes to pillar
		p1Epoch3BackerZnn    int64 = 0
		p1Epoch4PillarZnn    int64 = 900000000 // 40/0 split: toGive = 40% of 833,333,330 = 333,333,332; pillar gets 899,999,998
		p2PerEpochPillarZnn  int64 = 616666665 // 50/50 split of 1,233,333,330
		p2PerEpochControlZnn int64 = 616666665
	)
	_ = p1Epoch4PillarZnn

	for ep := uint64(1); ep <= 5; ep++ {
		ctx.pillarDelegations[ep] = delegations
		ctx.epochStats[ep] = &api.EpochStats{
			Epoch:       ep,
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

	// In epoch 2 (frontier), after epoch 1 has ended and before epoch 1 is settled:
	// Pillar 1 flips give-percentages from 0/100 to 0/0.
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

	// --- Settle Epoch 1 ---
	if err := computeDetailedPillarReward(ctx, 1); err != nil {
		t.Fatalf("computeDetailedPillarReward epoch 1 failed: %v", err)
	}

	// Check PillarEpochHistory for epoch 1:
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

	// Pin Epoch 1 RewardDepositHistory and cumulative RewardDeposit amounts and totals:
	p1Znn1 := assertEpochRewardHistory(t, ctx.Storage(), 1, stakeP1, p1Epoch12PillarZnn)
	b1Znn1 := assertEpochRewardHistory(t, ctx.Storage(), 1, backer1, p1Epoch12Backer1Znn)
	b2Znn1 := assertEpochRewardHistory(t, ctx.Storage(), 1, backer2, p1Epoch12Backer2Znn)
	p1Total1 := new(big.Int).Add(p1Znn1, new(big.Int).Add(b1Znn1, b2Znn1))
	if p1Total1.Cmp(big.NewInt(pillarTotalReward)) != 0 {
		t.Fatalf("epoch 1 pillar_one total credit mismatch: want %d, got %v", pillarTotalReward, p1Total1)
	}

	p2Znn1 := assertEpochRewardHistory(t, ctx.Storage(), 1, stakeP2, p2PerEpochPillarZnn)
	ctrlZnn1 := assertEpochRewardHistory(t, ctx.Storage(), 1, controlBacker, p2PerEpochControlZnn)
	if new(big.Int).Add(p2Znn1, ctrlZnn1).Cmp(big.NewInt(pillarTotalReward)) != 0 {
		t.Fatalf("epoch 1 pillar_two_unchanged total credit mismatch")
	}

	assertCumulativeRewardDeposit(t, ctx.Storage(), stakeP1, p1Epoch12PillarZnn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), backer1, p1Epoch12Backer1Znn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), backer2, p1Epoch12Backer2Znn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), stakeP2, p2PerEpochPillarZnn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), controlBacker, p2PerEpochControlZnn)

	// --- Settle Epoch 2 (update occurred in epoch 2, so 0/100 still governs epoch 2) ---
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

	p1Znn2 := assertEpochRewardHistory(t, ctx.Storage(), 2, stakeP1, p1Epoch12PillarZnn)
	b1Znn2 := assertEpochRewardHistory(t, ctx.Storage(), 2, backer1, p1Epoch12Backer1Znn)
	b2Znn2 := assertEpochRewardHistory(t, ctx.Storage(), 2, backer2, p1Epoch12Backer2Znn)
	if new(big.Int).Add(p1Znn2, new(big.Int).Add(b1Znn2, b2Znn2)).Cmp(big.NewInt(pillarTotalReward)) != 0 {
		t.Fatalf("epoch 2 pillar_one total credit mismatch")
	}
	assertCumulativeRewardDeposit(t, ctx.Storage(), stakeP1, 2*p1Epoch12PillarZnn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), backer1, 2*p1Epoch12Backer1Znn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), backer2, 2*p1Epoch12Backer2Znn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), stakeP2, 2*p2PerEpochPillarZnn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), controlBacker, 2*p2PerEpochControlZnn)

	// --- Settle Epoch 3 (0/0 takes effect) ---
	// Also in epoch 3, update only GiveBlockRewardPercentage to 40/0 (effective in epoch 4).
	ctx.setEpoch(3)
	sendBlock.Data = definition.ABIPillars.PackMethodPanic(
		definition.UpdatePillarMethodName,
		"pillar_one",
		stakeP1,
		stakeP1,
		uint8(40),
		uint8(0),
	)
	if _, err := method.ReceiveBlock(ctx, sendBlock); err != nil {
		t.Fatalf("epoch 3 block-only ReceiveBlock failed: %v", err)
	}

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

	p1Znn3 := assertEpochRewardHistory(t, ctx.Storage(), 3, stakeP1, p1Epoch3PillarZnn)
	b1Znn3 := assertEpochRewardHistory(t, ctx.Storage(), 3, backer1, p1Epoch3BackerZnn)
	b2Znn3 := assertEpochRewardHistory(t, ctx.Storage(), 3, backer2, p1Epoch3BackerZnn)
	if new(big.Int).Add(p1Znn3, new(big.Int).Add(b1Znn3, b2Znn3)).Cmp(big.NewInt(pillarTotalReward)) != 0 {
		t.Fatalf("epoch 3 pillar_one total credit mismatch")
	}
	assertEpochRewardHistory(t, ctx.Storage(), 3, stakeP2, p2PerEpochPillarZnn)
	assertEpochRewardHistory(t, ctx.Storage(), 3, controlBacker, p2PerEpochControlZnn)

	cumP1After3 := 2*p1Epoch12PillarZnn + p1Epoch3PillarZnn
	cumB1After3 := 2 * p1Epoch12Backer1Znn
	cumB2After3 := 2 * p1Epoch12Backer2Znn
	assertCumulativeRewardDeposit(t, ctx.Storage(), stakeP1, cumP1After3)
	assertCumulativeRewardDeposit(t, ctx.Storage(), backer1, cumB1After3)
	assertCumulativeRewardDeposit(t, ctx.Storage(), backer2, cumB2After3)
	if cumP1After3+cumB1After3+cumB2After3 != 3*pillarTotalReward {
		t.Fatalf("3-epoch cumulative total mismatch")
	}

	// --- Settle Epoch 4 (40/0 takes effect) and update only GiveDelegateRewardPercentage to 40/25 for Epoch 5 ---
	// Under 40/0:
	//   toGiveN = (40 * 833,333,330 + 0) / 100 = 333,333,332
	//   pillar_one gets 1,233,333,330 - 333,333,332 = 899,999,998
	//   backer1 (60%) gets 333,333,332 * 600 / 1000 = 199,999,999
	//   backer2 (40%) gets 333,333,332 * 400 / 1000 = 133,333,332 (1 unit integer rounding from 333,333,332 split)
	ctx.setEpoch(4)
	sendBlock.Data = definition.ABIPillars.PackMethodPanic(
		definition.UpdatePillarMethodName,
		"pillar_one",
		stakeP1,
		stakeP1,
		uint8(40),
		uint8(25),
	)
	if _, err := method.ReceiveBlock(ctx, sendBlock); err != nil {
		t.Fatalf("epoch 4 delegate-only ReceiveBlock failed: %v", err)
	}

	if err := computeDetailedPillarReward(ctx, 4); err != nil {
		t.Fatalf("computeDetailedPillarReward epoch 4 failed: %v", err)
	}
	assertEpochRewardHistory(t, ctx.Storage(), 4, stakeP1, 899999998)
	assertEpochRewardHistory(t, ctx.Storage(), 4, backer1, 199999999)
	assertEpochRewardHistory(t, ctx.Storage(), 4, backer2, 133333332)

	// --- Settle Epoch 5 (40/25 takes effect) ---
	// Under 40/25:
	//   toGiveN = (40 * 833,333,330 + 25 * 400,000,000) / 100 = 333,333,332 + 100,000,000 = 433,333,332
	//   pillar_one gets 1,233,333,330 - 433,333,332 = 799,999,998
	//   backer1 (60%) gets 433,333,332 * 600 / 1000 = 259,999,999
	//   backer2 (40%) gets 433,333,332 * 400 / 1000 = 173,333,332
	if err := computeDetailedPillarReward(ctx, 5); err != nil {
		t.Fatalf("computeDetailedPillarReward epoch 5 failed: %v", err)
	}
	assertEpochRewardHistory(t, ctx.Storage(), 5, stakeP1, 799999998)
	assertEpochRewardHistory(t, ctx.Storage(), 5, backer1, 259999999)
	assertEpochRewardHistory(t, ctx.Storage(), 5, backer2, 173333332)
}

// TestPillar_SeveralUnsettledEpochsUnaffected tests that with several epochs
// (e.g. 5, 6, 7) unsettled, a change made in epoch 7 does not affect any of them,
// pinning both PillarEpochHistory and RewardDeposit credits.
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

	// Single pillar in epoch stats:
	//   BlockReward      = 83,333,333 * 10 = 833,333,330
	//   DelegationReward = 40,000,000 * 10 * 1000 * 10 / 10 / 1000 = 400,000,000
	//   TotalReward      = 1,233,333,330
	// At 40/60:
	//   toGiveN = (40 * 833,333,330 + 60 * 400,000,000) / 100 = 333,333,332 + 240,000,000 = 573,333,332
	//   pillarZnn = 1,233,333,330 - 573,333,332 = 659,999,998
	// At 5/5:
	//   toGiveN = (5 * 833,333,330 + 5 * 400,000,000) / 100 = 61,666,666
	//   pillarZnn = 1,233,333,330 - 61,666,666 = 1,171,666,664
	const (
		totalReward    int64 = 1233333330
		ep567BackerZnn int64 = 573333332
		ep567PillarZnn int64 = 659999998
		ep8BackerZnn   int64 = 61666666
		ep8PillarZnn   int64 = 1171666664
	)

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
	for idx, ep := range []uint64{5, 6, 7} {
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

		pZnn := assertEpochRewardHistory(t, ctx.Storage(), ep, stakeP1, ep567PillarZnn)
		bZnn := assertEpochRewardHistory(t, ctx.Storage(), ep, backer, ep567BackerZnn)
		if new(big.Int).Add(pZnn, bZnn).Cmp(big.NewInt(totalReward)) != 0 {
			t.Fatalf("epoch %d total reward mismatch", ep)
		}
		count := int64(idx + 1)
		assertCumulativeRewardDeposit(t, ctx.Storage(), stakeP1, count*ep567PillarZnn)
		assertCumulativeRewardDeposit(t, ctx.Storage(), backer, count*ep567BackerZnn)
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
	pZnn8 := assertEpochRewardHistory(t, ctx.Storage(), 8, stakeP1, ep8PillarZnn)
	bZnn8 := assertEpochRewardHistory(t, ctx.Storage(), 8, backer, ep8BackerZnn)
	if new(big.Int).Add(pZnn8, bZnn8).Cmp(big.NewInt(totalReward)) != 0 {
		t.Fatalf("epoch 8 total reward mismatch")
	}
	assertCumulativeRewardDeposit(t, ctx.Storage(), stakeP1, 3*ep567PillarZnn+ep8PillarZnn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), backer, 3*ep567BackerZnn+ep8BackerZnn)
}

// TestPillar_ActivationTransition tests that:
//  1. A change executed before activation followed by settlement after activation
//     settles with live values (as today).
//  2. Epochs settled before activation are untouched.
//  3. Subsequent post-activation updates preserve unsettled terms correctly.
//  4. All settled epochs credit exact RewardDeposit and RewardDepositHistory amounts.
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

	// Single pillar per epoch: BlockReward = 833,333,330, DelegationReward = 400,000,000, TotalReward = 1,233,333,330.
	// - At 10/90 (Epoch 1):
	//     toGiveN   = (10 * 833,333,330 + 90 * 400,000,000) / 100 = 83,333,333 + 360,000,000 = 443,333,333
	//     pillarZnn = 1,233,333,330 - 443,333,333 = 789,999,997
	// - At 25/75 (Epochs 2 & 3):
	//     toGiveN   = (25 * 833,333,330 + 75 * 400,000,000) / 100 = 508,333,332
	//     pillarZnn = 1,233,333,330 - 508,333,332 = 724,999,998
	// - At 70/30 (Epoch 4):
	//     toGiveN   = (70 * 833,333,330 + 30 * 400,000,000) / 100 = 583,333,331 + 120,000,000 = 703,333,331
	//     pillarZnn = 1,233,333,330 - 703,333,331 = 529,999,999
	const (
		totalReward   int64 = 1233333330
		ep1BackerZnn  int64 = 443333333
		ep1PillarZnn  int64 = 789999997
		ep23BackerZnn int64 = 508333332
		ep23PillarZnn int64 = 724999998
		ep4BackerZnn  int64 = 703333331
		ep4PillarZnn  int64 = 529999999
	)

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
	pZnn1 := assertEpochRewardHistory(t, ctx.Storage(), 1, stakeP1, ep1PillarZnn)
	bZnn1 := assertEpochRewardHistory(t, ctx.Storage(), 1, backer, ep1BackerZnn)
	if new(big.Int).Add(pZnn1, bZnn1).Cmp(big.NewInt(totalReward)) != 0 {
		t.Fatalf("epoch 1 total reward mismatch")
	}
	assertCumulativeRewardDeposit(t, ctx.Storage(), stakeP1, ep1PillarZnn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), backer, ep1BackerZnn)

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
	pZnn2 := assertEpochRewardHistory(t, ctx.Storage(), 2, stakeP1, ep23PillarZnn)
	bZnn2 := assertEpochRewardHistory(t, ctx.Storage(), 2, backer, ep23BackerZnn)
	if new(big.Int).Add(pZnn2, bZnn2).Cmp(big.NewInt(totalReward)) != 0 {
		t.Fatalf("epoch 2 total reward mismatch")
	}

	// Pre-activation settled epoch 1 history and reward history are untouched:
	hist1Check, err := definition.GetPillarEpochHistoryList(ctx.Storage(), 1)
	if err != nil || hist1Check[0].GiveBlockRewardPercentage != 10 || hist1Check[0].GiveDelegateRewardPercentage != 90 {
		t.Fatalf("epoch 1 pre-activation settlement altered: %v", hist1Check)
	}
	assertEpochRewardHistory(t, ctx.Storage(), 1, stakeP1, ep1PillarZnn)
	assertEpochRewardHistory(t, ctx.Storage(), 1, backer, ep1BackerZnn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), stakeP1, ep1PillarZnn+ep23PillarZnn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), backer, ep1BackerZnn+ep23BackerZnn)

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
	pZnn3 := assertEpochRewardHistory(t, ctx.Storage(), 3, stakeP1, ep23PillarZnn)
	bZnn3 := assertEpochRewardHistory(t, ctx.Storage(), 3, backer, ep23BackerZnn)
	if new(big.Int).Add(pZnn3, bZnn3).Cmp(big.NewInt(totalReward)) != 0 {
		t.Fatalf("epoch 3 total reward mismatch")
	}
	assertCumulativeRewardDeposit(t, ctx.Storage(), stakeP1, ep1PillarZnn+2*ep23PillarZnn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), backer, ep1BackerZnn+2*ep23BackerZnn)

	// Settle epoch 4: should settle with 70/30
	if err := computeDetailedPillarReward(ctx, 4); err != nil {
		t.Fatal(err)
	}
	hist4, err := definition.GetPillarEpochHistoryList(ctx.Storage(), 4)
	if err != nil || hist4[0].GiveBlockRewardPercentage != 70 || hist4[0].GiveDelegateRewardPercentage != 30 {
		t.Fatalf("epoch 4 expected 70/30, got %d/%d", hist4[0].GiveBlockRewardPercentage, hist4[0].GiveDelegateRewardPercentage)
	}
	pZnn4 := assertEpochRewardHistory(t, ctx.Storage(), 4, stakeP1, ep4PillarZnn)
	bZnn4 := assertEpochRewardHistory(t, ctx.Storage(), 4, backer, ep4BackerZnn)
	if new(big.Int).Add(pZnn4, bZnn4).Cmp(big.NewInt(totalReward)) != 0 {
		t.Fatalf("epoch 4 total reward mismatch")
	}
	assertCumulativeRewardDeposit(t, ctx.Storage(), stakeP1, ep1PillarZnn+2*ep23PillarZnn+ep4PillarZnn)
	assertCumulativeRewardDeposit(t, ctx.Storage(), backer, ep1BackerZnn+2*ep23BackerZnn+ep4BackerZnn)
}

// failingStorageAccount wraps a store.Account so Storage().Put fails when
// shouldFailPut returns an error for the key being written. Snapshot() preserves
// the wrapper so VM boundary Save()/Reset()/Done() snapshots inherit the failure hook.
type failingStorageAccount struct {
	store.Account
	shouldFailPut func(key []byte) error
}

type failingPutDB struct {
	db.DB
	shouldFailPut func(key []byte) error
}

func (f *failingPutDB) Put(key, value []byte) error {
	if f.shouldFailPut != nil {
		if err := f.shouldFailPut(key); err != nil {
			return err
		}
	}
	return f.DB.Put(key, value)
}

func (f *failingStorageAccount) Storage() db.DB {
	return &failingPutDB{
		DB:            f.Account.Storage(),
		shouldFailPut: f.shouldFailPut,
	}
}

func (f *failingStorageAccount) Snapshot() store.Account {
	return &failingStorageAccount{
		Account:       f.Account.Snapshot(),
		shouldFailPut: f.shouldFailPut,
	}
}

// executeAcrossVMBoundary runs an embedded method inside the same Save / Reset / Done
// snapshot lifecycle used by vm.generateEmbeddedReceive.
func executeAcrossVMBoundary(ctx *testPillarContext, method interface {
	ReceiveBlock(context *testPillarContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error)
}, sendBlock *nom.AccountBlock) error {
	ctx.Save()
	_, err := method.ReceiveBlock(ctx, sendBlock)
	if err != nil {
		ctx.Reset()
		return err
	}
	ctx.Done()
	return nil
}

// TestPillar_WriteFailuresAndVMRollback covers storage write failures in the
// baseline PillarEpochTerms path, the new PillarEpochTerms path, and the
// PillarEpochHistory settlement path, verifying that each failure aborts the
// call and rolls back all earlier writes across the VM snapshot boundary.
func TestPillar_WriteFailuresAndVMRollback(t *testing.T) {
	errInjectedWrite := errors.New("injected storage write failure")
	pillarName := "rollback_pillar"
	stakeAddr := testUserAddress(40)
	initProducer := testUserAddress(41)
	newProducer := testUserAddress(42)
	rewardAddr := testUserAddress(43)
	backer := testUserAddress(44)

	newSeededContext := func(t *testing.T) (*testPillarContext, *failingStorageAccount) {
		t.Helper()
		ctx := newTestPillarContext()
		fa := &failingStorageAccount{Account: ctx.Account}
		ctx.Account = fa

		initPillar := &definition.PillarInfo{
			Name:                         pillarName,
			BlockProducingAddress:        initProducer,
			RewardWithdrawAddress:        rewardAddr,
			StakeAddress:                 stakeAddr,
			Amount:                       constants.PillarStakeAmount,
			RegistrationTime:             100,
			RevokeTime:                   0,
			GiveBlockRewardPercentage:    50,
			GiveDelegateRewardPercentage: 50,
			PillarType:                   definition.NormalPillarType,
		}
		if err := initPillar.Save(ctx.Storage()); err != nil {
			t.Fatal(err)
		}
		if err := (&definition.ProducingPillar{Name: pillarName, Producing: &initProducer}).Save(ctx.Storage()); err != nil {
			t.Fatal(err)
		}
		return ctx, fa
	}

	updateMethod := &UpdatePillarMethod{MethodName: definition.UpdatePillarMethodName}
	baselineKey := definition.GetPillarEpochTermsKey(pillarName, 0)
	epoch5TermsKey := definition.GetPillarEpochTermsKey(pillarName, 5)

	t.Run("baseline_write_failure_aborts_and_rolls_back_producer_index", func(t *testing.T) {
		ctx, fa := newSeededContext(t)
		ctx.setEpoch(4)

		fa.shouldFailPut = func(key []byte) error {
			if bytes.Equal(key, baselineKey) {
				return errInjectedWrite
			}
			return nil
		}

		sendBlock := &nom.AccountBlock{
			Address:   stakeAddr,
			ToAddress: types.PillarContract,
			Amount:    big.NewInt(0),
			Data: definition.ABIPillars.PackMethodPanic(
				definition.UpdatePillarMethodName,
				pillarName,
				newProducer, // triggers ProducingPillar.Save before baseline.Save
				rewardAddr,
				uint8(80),
				uint8(10),
			),
		}

		ctx.Save()
		_, err := updateMethod.ReceiveBlock(ctx, sendBlock)
		if !errors.Is(err, errInjectedWrite) {
			t.Fatalf("want errInjectedWrite, got %v", err)
		}
		ctx.Reset()

		// Verify rollback: newProducer index entry was rolled back, no terms exist, and PillarInfo is still 50/50.
		if _, err := definition.GetProducingPillarName(ctx.Storage(), newProducer); err != constants.ErrDataNonExistent {
			t.Fatalf("expected newProducer index write to be rolled back, got err=%v", err)
		}
		if has, err := definition.HasPillarEpochTerms(ctx.Storage(), pillarName); err != nil || has {
			t.Fatalf("expected no PillarEpochTerms after rollback, got has=%v err=%v", has, err)
		}
		pillarAfter, err := definition.GetPillarInfo(ctx.Storage(), pillarName)
		if err != nil {
			t.Fatal(err)
		}
		if pillarAfter.GiveBlockRewardPercentage != 50 || pillarAfter.GiveDelegateRewardPercentage != 50 || pillarAfter.BlockProducingAddress != initProducer {
			t.Fatalf("expected PillarInfo untouched after rollback, got %+v", pillarAfter)
		}
	})

	t.Run("new_terms_write_failure_aborts_and_rolls_back_baseline_and_producer", func(t *testing.T) {
		ctx, fa := newSeededContext(t)
		ctx.setEpoch(4)

		fa.shouldFailPut = func(key []byte) error {
			if bytes.Equal(key, epoch5TermsKey) {
				return errInjectedWrite
			}
			return nil
		}

		sendBlock := &nom.AccountBlock{
			Address:   stakeAddr,
			ToAddress: types.PillarContract,
			Amount:    big.NewInt(0),
			Data: definition.ABIPillars.PackMethodPanic(
				definition.UpdatePillarMethodName,
				pillarName,
				newProducer, // writes ProducingPillar first, then baseline at 0, then fails at epoch 5
				rewardAddr,
				uint8(80),
				uint8(10),
			),
		}

		ctx.Save()
		_, err := updateMethod.ReceiveBlock(ctx, sendBlock)
		if !errors.Is(err, errInjectedWrite) {
			t.Fatalf("want errInjectedWrite, got %v", err)
		}
		ctx.Reset()

		// Verify rollback across VM boundary reverted both baseline at epoch 0 and newProducer index.
		if _, err := definition.GetPillarEpochTerms(ctx.Storage(), pillarName, 0); err != constants.ErrDataNonExistent {
			t.Fatalf("expected baseline terms at epoch 0 to be rolled back, got err=%v", err)
		}
		if _, err := definition.GetProducingPillarName(ctx.Storage(), newProducer); err != constants.ErrDataNonExistent {
			t.Fatalf("expected newProducer index write to be rolled back, got err=%v", err)
		}
		pillarAfter, err := definition.GetPillarInfo(ctx.Storage(), pillarName)
		if err != nil {
			t.Fatal(err)
		}
		if pillarAfter.GiveBlockRewardPercentage != 50 || pillarAfter.GiveDelegateRewardPercentage != 50 || pillarAfter.BlockProducingAddress != initProducer {
			t.Fatalf("expected PillarInfo untouched after rollback, got %+v", pillarAfter)
		}
	})

	t.Run("epoch_history_write_failure_aborts_and_rolls_back_epoch_settlement", func(t *testing.T) {
		ctx, fa := newSeededContext(t)
		ctx.pillarDelegations[0] = map[string]*types.PillarDelegationDetail{
			pillarName: {
				PillarDelegation: types.PillarDelegation{Name: pillarName},
				Backers:          map[types.Address]*big.Int{backer: big.NewInt(1000)},
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

		// Set frontier momentum past epoch 0's end + RewardTimeLimit AND height >= UpdateMinNumMomentums
		_, epoch0End := ctx.ticker.ToTime(0)
		readyTime := epoch0End.Add(time.Duration(constants.RewardTimeLimit+10) * time.Second)
		ctx.momentum.Timestamp = &readyTime
		ctx.momentum.Height = constants.UpdateMinNumMomentums + 5

		epoch0HistKey := definition.GetPillarEpochHistoryKey(0, pillarName)
		fa.shouldFailPut = func(key []byte) error {
			if bytes.Equal(key, epoch0HistKey) {
				return errInjectedWrite
			}
			return nil
		}

		embeddedUpdate := &UpdateEmbeddedPillarMethod{MethodName: definition.UpdateMethodName}
		updateSend := &nom.AccountBlock{
			Address:   stakeAddr,
			ToAddress: types.PillarContract,
			Amount:    big.NewInt(0),
			Data:      definition.ABICommon.PackMethodPanic(definition.UpdateMethodName),
		}

		ctx.Save()
		_, err := embeddedUpdate.ReceiveBlock(ctx, updateSend)
		if !errors.Is(err, errInjectedWrite) {
			t.Fatalf("want errInjectedWrite from PillarEpochHistory.Save, got %v", err)
		}
		ctx.Reset()

		// Verify LastUpdate, LastEpochUpdate, PillarEpochHistory, and RewardDeposit were all rolled back.
		lastEpoch, err := definition.GetLastEpochUpdate(ctx.Storage())
		if err != nil {
			t.Fatal(err)
		}
		if lastEpoch.LastEpoch != -1 {
			t.Fatalf("expected LastEpochUpdate rolled back to -1, got %d", lastEpoch.LastEpoch)
		}
		lastUpdate, err := definition.GetLastUpdate(ctx.Storage())
		if err != nil {
			t.Fatal(err)
		}
		if lastUpdate.Height != 0 {
			t.Fatalf("expected LastUpdate height rolled back to 0, got %d", lastUpdate.Height)
		}
		histList, err := definition.GetPillarEpochHistoryList(ctx.Storage(), 0)
		if err != nil || len(histList) != 0 {
			t.Fatalf("expected 0 epoch history entries after rollback, got %d (err=%v)", len(histList), err)
		}
		assertCumulativeRewardDeposit(t, ctx.Storage(), rewardAddr, 0)
		assertCumulativeRewardDeposit(t, ctx.Storage(), backer, 0)
	})
}
