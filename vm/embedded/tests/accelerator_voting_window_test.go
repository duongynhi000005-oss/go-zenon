package tests

import (
	"math/big"
	"testing"
	"time"

	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api/embedded"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

func saveAcceleratorVotingPeriod(t *testing.T, period int64) {
	saved := constants.AcceleratorProjectVotingPeriod
	constants.AcceleratorProjectVotingPeriod = period
	t.Cleanup(func() {
		constants.AcceleratorProjectVotingPeriod = saved
	})
}

func TestAccelerator_ElapsedVotingWindow_PreSporkClosesPassingProject(t *testing.T) {
	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	defer z.StopPanic()
	saveAcceleratorVotingPeriod(t, 2000)

	activateAccelerator(t, z)
	acceleratorAPI := embedded.NewAcceleratorApi(z)

	defer z.CallContract(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.AcceleratorContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        constants.ProjectCreationAmount,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.CreateProjectMethodName,
			"PreSpork Elapsed",
			"TEST DESCRIPTION",
			"test.com",
			big.NewInt(100),
			big.NewInt(1000),
		),
	}).Error(t, nil)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	projectList, err := acceleratorAPI.GetAll(0, 10)
	common.FailIfErr(t, err)
	projectID := projectList.List[0].Id

	defer z.CallContract(&nom.AccountBlock{
		Address:   g.Pillar1.Address,
		ToAddress: types.AcceleratorContract,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.VoteByNameMethodName,
			projectID,
			g.Pillar1Name,
			definition.VoteYes,
		),
	}).Error(t, nil)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	defer z.CallContract(&nom.AccountBlock{
		Address:   g.Pillar2.Address,
		ToAddress: types.AcceleratorContract,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.VoteByProdAddressMethodName,
			projectID,
			definition.VoteYes,
		),
	}).Error(t, nil)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	// First update runs at height 361 (timestamp 1000003600 > 1000000200 + 2000).
	z.InsertMomentumsTo(60*6 + 2)

	project, err := acceleratorAPI.GetProjectById(projectID)
	common.FailIfErr(t, err)
	if project.Status != definition.ClosedStatus {
		t.Fatalf("pre-spork: want status %d (ClosedStatus), got %d", definition.ClosedStatus, project.Status)
	}
}

func TestAccelerator_ElapsedVotingWindow_PostSporkFinalTallyAndLateVoteRejection(t *testing.T) {
	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	defer z.StopPanic()
	saveAcceleratorVotingPeriod(t, 2000)

	activateAccelerator(t, z)
	activateDynamicPlasmaPastHeight(t, z)
	acceleratorAPI := embedded.NewAcceleratorApi(z)

	// Project 1: receives passing votes inside the window; window elapses before first Update -> ActiveStatus.
	defer z.CallContract(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.AcceleratorContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        constants.ProjectCreationAmount,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.CreateProjectMethodName,
			"Passing Elapsed",
			"TEST DESCRIPTION",
			"test.com",
			big.NewInt(100),
			big.NewInt(1000),
		),
	}).Error(t, nil)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	// Project 2: receives only 1 of 3 votes inside the window; late votes after expiry are rejected -> ClosedStatus.
	defer z.CallContract(&nom.AccountBlock{
		Address:       g.User2.Address,
		ToAddress:     types.AcceleratorContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        constants.ProjectCreationAmount,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.CreateProjectMethodName,
			"Failing Elapsed",
			"TEST DESCRIPTION",
			"test.com",
			big.NewInt(100),
			big.NewInt(1000),
		),
	}).Error(t, nil)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	projectList, err := acceleratorAPI.GetAll(0, 10)
	common.FailIfErr(t, err)
	if projectList.Count != 2 {
		t.Fatalf("want 2 projects, got %d", projectList.Count)
	}
	var passingID, failingID types.Hash
	var passingCreateTime, failingCreateTime int64
	for _, p := range projectList.List {
		if p.Name == "Passing Elapsed" {
			passingID = p.Id
			passingCreateTime = p.CreationTimestamp
		} else {
			failingID = p.Id
			failingCreateTime = p.CreationTimestamp
		}
	}

	// Cast passing votes on Project 1 inside its window, and 1 vote on Project 2.
	defer z.CallContract(&nom.AccountBlock{
		Address:   g.Pillar1.Address,
		ToAddress: types.AcceleratorContract,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.VoteByNameMethodName,
			passingID,
			g.Pillar1Name,
			definition.VoteYes,
		),
	}).Error(t, nil)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	defer z.CallContract(&nom.AccountBlock{
		Address:   g.Pillar2.Address,
		ToAddress: types.AcceleratorContract,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.VoteByProdAddressMethodName,
			passingID,
			definition.VoteYes,
		),
	}).Error(t, nil)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	defer z.CallContract(&nom.AccountBlock{
		Address:   g.Pillar1.Address,
		ToAddress: types.AcceleratorContract,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.VoteByNameMethodName,
			failingID,
			g.Pillar1Name,
			definition.VoteNo,
		),
	}).Error(t, nil)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	// Advance so that the frontier momentum is at failingCreateTime + 2000 (exact expiry of failingID).
	for {
		fm, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
		common.FailIfErr(t, err)
		if fm.Timestamp.Unix() == failingCreateTime+2000 {
			break
		}
		z.InsertNewMomentum()
	}

	// Send block is created when frontier is at failingCreateTime + 2000 (inside window);
	// once cemented on the next momentum (failingCreateTime + 2010, past expiry), the contract receive is rejected.
	defer z.CallContract(&nom.AccountBlock{
		Address:   g.Pillar2.Address,
		ToAddress: types.AcceleratorContract,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.VoteByProdAddressMethodName,
			failingID,
			definition.VoteYes,
		),
	}).Error(t, constants.ErrForbiddenParam)
	z.InsertNewMomentum() // cements send block; frontier momentum advances to failingCreateTime + 2010 (past expiry)
	z.InsertNewMomentum() // contract receive executes against frontier at failingCreateTime + 2010 -> rejected

	// Both projects are now past their voting windows (passingCreateTime < failingCreateTime).
	// Direct post-expiry votes and vote overwrites via VoteByName and VoteByProdAddress are rejected.
	defer z.CallContract(&nom.AccountBlock{
		Address:   g.Pillar2.Address,
		ToAddress: types.AcceleratorContract,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.VoteByNameMethodName,
			failingID,
			g.Pillar2Name,
			definition.VoteYes,
		),
	}).Error(t, constants.ErrForbiddenParam)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	// Attempt to overwrite passingID's Yes vote with a No vote after expiry via VoteByName and VoteByProdAddress.
	defer z.CallContract(&nom.AccountBlock{
		Address:   g.Pillar1.Address,
		ToAddress: types.AcceleratorContract,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.VoteByNameMethodName,
			passingID,
			g.Pillar1Name,
			definition.VoteNo,
		),
	}).Error(t, constants.ErrForbiddenParam)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	defer z.CallContract(&nom.AccountBlock{
		Address:   g.Pillar2.Address,
		ToAddress: types.AcceleratorContract,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.VoteByProdAddressMethodName,
			passingID,
			definition.VoteNo,
		),
	}).Error(t, constants.ErrForbiddenParam)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	// Advance past height 361 so the first Update runs after both windows have elapsed.
	z.InsertMomentumsTo(60*6 + 2)

	passingProj, err := acceleratorAPI.GetProjectById(passingID)
	common.FailIfErr(t, err)
	if passingProj.Status != definition.ActiveStatus {
		t.Fatalf("post-spork passing project: want status %d (ActiveStatus), got %d (create=%d)", definition.ActiveStatus, passingProj.Status, passingCreateTime)
	}

	failingProj, err := acceleratorAPI.GetProjectById(failingID)
	common.FailIfErr(t, err)
	if failingProj.Status != definition.ClosedStatus {
		t.Fatalf("post-spork failing project: want status %d (ClosedStatus), got %d", definition.ClosedStatus, failingProj.Status)
	}

	// Add a phase to the newly active project and verify phase voting is unaffected after AcceleratorProjectVotingPeriod.
	defer z.CallContract(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.AcceleratorContract,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.AddPhaseMethodName,
			passingID,
			"Phase 1",
			"Phase 1 description",
			"test.com",
			big.NewInt(10),
			big.NewInt(10),
		),
	}).Error(t, nil)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	passingProj, err = acceleratorAPI.GetProjectById(passingID)
	common.FailIfErr(t, err)
	phaseID := passingProj.PhaseIds[0]

	// Advance more than 2000s (200 momentums) past phase creation; phase votes still succeed.
	for i := 0; i < 210; i++ {
		z.InsertNewMomentum()
	}

	defer z.CallContract(&nom.AccountBlock{
		Address:   g.Pillar1.Address,
		ToAddress: types.AcceleratorContract,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.VoteByNameMethodName,
			phaseID,
			g.Pillar1Name,
			definition.VoteYes,
		),
	}).Error(t, nil)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	defer z.CallContract(&nom.AccountBlock{
		Address:   g.Pillar2.Address,
		ToAddress: types.AcceleratorContract,
		Data: definition.ABIAccelerator.PackMethodPanic(definition.VoteByProdAddressMethodName,
			phaseID,
			definition.VoteYes,
		),
	}).Error(t, nil)
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	breakdown, err := acceleratorAPI.GetVoteBreakdown(phaseID)
	common.FailIfErr(t, err)
	if breakdown.Total != 2 || breakdown.Yes != 2 {
		t.Fatalf("want phase breakdown 2 yes, got %+v", breakdown)
	}
}
