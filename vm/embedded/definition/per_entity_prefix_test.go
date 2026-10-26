package definition

import (
	"errors"
	"math/big"
	"testing"

	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

type rowCountingIterator struct {
	db.StorageIterator
	rows     *int
	released *int
	iterErr  error
}

func (it *rowCountingIterator) Next() bool {
	if it.iterErr != nil {
		return false
	}
	hasNext := it.StorageIterator.Next()
	if hasNext {
		*it.rows++
	}
	return hasNext
}

func (it *rowCountingIterator) Error() error {
	if it.iterErr != nil {
		return it.iterErr
	}
	return it.StorageIterator.Error()
}

func (it *rowCountingIterator) Release() {
	*it.released++
	it.StorageIterator.Release()
}

type rowCountingDB struct {
	db.DB
	opened   int
	rows     int
	released int
	iterErr  error
}

func (c *rowCountingDB) reset() {
	c.opened = 0
	c.rows = 0
	c.released = 0
}

func (c *rowCountingDB) NewIterator(prefix []byte) db.StorageIterator {
	c.opened++
	return &rowCountingIterator{
		StorageIterator: c.DB.NewIterator(prefix),
		rows:            &c.rows,
		released:        &c.released,
		iterErr:         c.iterErr,
	}
}

func legacyGetStakeListByAddress(context db.DB, address types.Address) ([]*StakeInfo, *big.Int, *big.Int, error) {
	total := big.NewInt(0)
	weighted := big.NewInt(0)
	list := make([]*StakeInfo, 0)

	err := IterateStakeEntries(context, func(stakeInfo *StakeInfo) error {
		if stakeInfo.RevokeTime == 0 && stakeInfo.StakeAddress == address {
			list = append(list, stakeInfo)
			total.Add(total, stakeInfo.Amount)
			weighted.Add(weighted, stakeInfo.WeightedAmount)
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return list, total, weighted, nil
}

func legacyGetLiquidityStakeListByAddress(context db.DB, address types.Address) ([]*LiquidityStakeEntry, *big.Int, *big.Int, error) {
	total := big.NewInt(0)
	weighted := big.NewInt(0)
	list := make([]*LiquidityStakeEntry, 0)

	err := IterateLiquidityStakeEntries(context, func(stakeEntry *LiquidityStakeEntry) error {
		if stakeEntry.RevokeTime == 0 && stakeEntry.StakeAddress == address {
			list = append(list, stakeEntry)
			total.Add(total, stakeEntry.Amount)
			weighted.Add(weighted, stakeEntry.WeightedAmount)
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return list, total, weighted, nil
}

func legacyGetAllPillarVotes(context db.DB, id types.Hash) []*PillarVote {
	iterator := context.NewIterator(pillarVoteKeyPrefix)
	defer iterator.Release()

	pillarVoteList := make([]*PillarVote, 0)
	for {
		if !iterator.Next() {
			common.DealWithErr(iterator.Error())
			break
		}
		pillarVote, err := parsePillarVote(iterator.Value())
		if err != nil {
			continue
		}
		if pillarVote.Id == id {
			pillarVoteList = append(pillarVoteList, pillarVote)
		}
	}
	return pillarVoteList
}

func makeAddressWithLastByte(prefix [19]byte, last byte) types.Address {
	var raw [types.AddressSize]byte
	copy(raw[:19], prefix[:])
	raw[19] = last
	var addr types.Address
	common.DealWithErr(addr.SetBytes(raw[:]))
	return addr
}

func makeHashWithLastByte(prefix [31]byte, last byte) types.Hash {
	var raw [types.HashSize]byte
	copy(raw[:31], prefix[:])
	raw[31] = last
	return types.BytesToHashPanic(raw[:])
}

func TestGetStakeListByAddress_PerEntityPrefixScan(t *testing.T) {
	mem := db.NewMemDB()
	sharedPrefix := [19]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19}
	addrA := makeAddressWithLastByte(sharedPrefix, 0x10)
	addrB := makeAddressWithLastByte(sharedPrefix, 0x11) // shares 19-byte prefix with addrA
	addrC := makeAddressWithLastByte(sharedPrefix, 0x20)
	addrEmpty := makeAddressWithLastByte(sharedPrefix, 0x99)

	entries := []*StakeInfo{
		{StakeAddress: addrA, Id: types.NewHash([]byte{3}), Amount: big.NewInt(100), WeightedAmount: big.NewInt(150), StartTime: 10, RevokeTime: 0, ExpirationTime: 100},
		{StakeAddress: addrA, Id: types.NewHash([]byte{1}), Amount: big.NewInt(200), WeightedAmount: big.NewInt(400), StartTime: 20, RevokeTime: 0, ExpirationTime: 200},
		{StakeAddress: addrA, Id: types.NewHash([]byte{2}), Amount: big.NewInt(500), WeightedAmount: big.NewInt(900), StartTime: 15, RevokeTime: 55, ExpirationTime: 90}, // revoked
		{StakeAddress: addrB, Id: types.NewHash([]byte{1}), Amount: big.NewInt(300), WeightedAmount: big.NewInt(300), StartTime: 10, RevokeTime: 0, ExpirationTime: 100},
		{StakeAddress: addrB, Id: types.NewHash([]byte{2}), Amount: big.NewInt(400), WeightedAmount: big.NewInt(800), StartTime: 20, RevokeTime: 0, ExpirationTime: 200},
		{StakeAddress: addrB, Id: types.NewHash([]byte{3}), Amount: big.NewInt(700), WeightedAmount: big.NewInt(700), StartTime: 30, RevokeTime: 99, ExpirationTime: 300}, // revoked
		{StakeAddress: addrC, Id: types.NewHash([]byte{1}), Amount: big.NewInt(50), WeightedAmount: big.NewInt(50), StartTime: 5, RevokeTime: 0, ExpirationTime: 50},
	}
	for _, e := range entries {
		if err := e.Save(mem); err != nil {
			t.Fatal(err)
		}
	}
	// Empty value inside addrA's range should be skipped (ErrDataNonExistent).
	emptyId := types.NewHash([]byte{99})
	if err := mem.Put(getStakeInfoKey(emptyId, addrA), []byte{}); err != nil {
		t.Fatal(err)
	}

	cdb := &rowCountingDB{DB: mem}

	for _, tc := range []struct {
		name     string
		addr     types.Address
		wantRows int
		wantLen  int
	}{
		{name: "addrA with active, revoked, and empty row", addr: addrA, wantRows: 4, wantLen: 2},
		{name: "addrB sharing 19-byte prefix", addr: addrB, wantRows: 3, wantLen: 2},
		{name: "addrC single row", addr: addrC, wantRows: 1, wantLen: 1},
		{name: "empty address", addr: addrEmpty, wantRows: 0, wantLen: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantList, wantTotal, wantWeighted, err := legacyGetStakeListByAddress(mem, tc.addr)
			if err != nil {
				t.Fatal(err)
			}

			cdb.reset()
			gotList, gotTotal, gotWeighted, err := GetStakeListByAddress(cdb, tc.addr)
			if err != nil {
				t.Fatal(err)
			}
			if cdb.opened != 1 || cdb.released != 1 {
				t.Fatalf("opened=%d released=%d, want 1 and 1", cdb.opened, cdb.released)
			}
			if cdb.rows != tc.wantRows {
				t.Fatalf("visited %d rows, want %d", cdb.rows, tc.wantRows)
			}
			if len(gotList) != tc.wantLen || len(gotList) != len(wantList) {
				t.Fatalf("len=%d, want %d", len(gotList), len(wantList))
			}
			if gotTotal.Cmp(wantTotal) != 0 || gotWeighted.Cmp(wantWeighted) != 0 {
				t.Fatalf("totals=(%s, %s), want (%s, %s)", gotTotal, gotWeighted, wantTotal, wantWeighted)
			}
			for i := range wantList {
				if gotList[i].Id != wantList[i].Id ||
					gotList[i].StakeAddress != wantList[i].StakeAddress ||
					gotList[i].Amount.Cmp(wantList[i].Amount) != 0 ||
					gotList[i].WeightedAmount.Cmp(wantList[i].WeightedAmount) != 0 {
					t.Fatalf("entry %d mismatch: got %+v, want %+v", i, gotList[i], wantList[i])
				}
			}
		})
	}

	// A malformed row outside addrA's range does not fail addrA's read, whereas a malformed row inside addrB's range surfaces an error and releases the iterator.
	if err := mem.Put(getStakeInfoKey(types.NewHash([]byte{77}), addrB), []byte{0xff, 0xfe}); err != nil {
		t.Fatal(err)
	}
	cdb.reset()
	if _, _, _, err := GetStakeListByAddress(cdb, addrA); err != nil {
		t.Fatalf("malformed row outside addrA range failed addrA read: %v", err)
	}
	if cdb.released != 1 {
		t.Fatalf("iterator not released: released=%d", cdb.released)
	}

	cdb.reset()
	if _, _, _, err := GetStakeListByAddress(cdb, addrB); err == nil {
		t.Fatal("expected error for malformed row inside addrB range, got nil")
	}
	if cdb.released != 1 {
		t.Fatalf("iterator not released on decode error: released=%d", cdb.released)
	}

	// Iterator error is surfaced and iterator is released.
	cdb.reset()
	cdb.iterErr = errors.New("simulated iterator failure")
	if _, _, _, err := GetStakeListByAddress(cdb, addrA); !errors.Is(err, cdb.iterErr) {
		t.Fatalf("want iterator error %v, got %v", cdb.iterErr, err)
	}
	if cdb.released != 1 {
		t.Fatalf("iterator not released on iterator error: released=%d", cdb.released)
	}
}

func TestGetLiquidityStakeListByAddress_PerEntityPrefixScan(t *testing.T) {
	mem := db.NewMemDB()
	sharedPrefix := [19]byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	addrA := makeAddressWithLastByte(sharedPrefix, 0x01)
	addrB := makeAddressWithLastByte(sharedPrefix, 0x02)
	addrEmpty := makeAddressWithLastByte(sharedPrefix, 0x03)

	entries := []*LiquidityStakeEntry{
		{StakeAddress: addrA, Id: types.NewHash([]byte{10}), TokenStandard: types.ZnnTokenStandard, Amount: big.NewInt(1000), WeightedAmount: big.NewInt(2000), StartTime: 1, RevokeTime: 0, ExpirationTime: 100},
		{StakeAddress: addrA, Id: types.NewHash([]byte{20}), TokenStandard: types.QsrTokenStandard, Amount: big.NewInt(500), WeightedAmount: big.NewInt(1500), StartTime: 2, RevokeTime: 0, ExpirationTime: 200},
		{StakeAddress: addrA, Id: types.NewHash([]byte{30}), TokenStandard: types.ZnnTokenStandard, Amount: big.NewInt(0), WeightedAmount: big.NewInt(900), StartTime: 3, RevokeTime: 50, ExpirationTime: 300}, // revoked
		{StakeAddress: addrB, Id: types.NewHash([]byte{10}), TokenStandard: types.ZnnTokenStandard, Amount: big.NewInt(750), WeightedAmount: big.NewInt(750), StartTime: 4, RevokeTime: 0, ExpirationTime: 400},
	}
	for _, e := range entries {
		if err := e.Save(mem); err != nil {
			t.Fatal(err)
		}
	}
	// Empty value inside addrA's range.
	if err := mem.Put(getLiquidityStakeEntryKey(types.NewHash([]byte{99}), addrA), []byte{}); err != nil {
		t.Fatal(err)
	}

	cdb := &rowCountingDB{DB: mem}

	for _, tc := range []struct {
		name     string
		addr     types.Address
		wantRows int
		wantLen  int
	}{
		{name: "addrA with active, revoked, and empty row", addr: addrA, wantRows: 4, wantLen: 2},
		{name: "addrB sharing 19-byte prefix", addr: addrB, wantRows: 1, wantLen: 1},
		{name: "empty address", addr: addrEmpty, wantRows: 0, wantLen: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantList, wantTotal, wantWeighted, err := legacyGetLiquidityStakeListByAddress(mem, tc.addr)
			if err != nil {
				t.Fatal(err)
			}

			cdb.reset()
			gotList, gotTotal, gotWeighted, err := GetLiquidityStakeListByAddress(cdb, tc.addr)
			if err != nil {
				t.Fatal(err)
			}
			if cdb.opened != 1 || cdb.released != 1 {
				t.Fatalf("opened=%d released=%d, want 1 and 1", cdb.opened, cdb.released)
			}
			if cdb.rows != tc.wantRows {
				t.Fatalf("visited %d rows, want %d", cdb.rows, tc.wantRows)
			}
			if len(gotList) != tc.wantLen || len(gotList) != len(wantList) {
				t.Fatalf("len=%d, want %d", len(gotList), len(wantList))
			}
			if gotTotal.Cmp(wantTotal) != 0 || gotWeighted.Cmp(wantWeighted) != 0 {
				t.Fatalf("totals=(%s, %s), want (%s, %s)", gotTotal, gotWeighted, wantTotal, wantWeighted)
			}
			for i := range wantList {
				if gotList[i].Id != wantList[i].Id ||
					gotList[i].StakeAddress != wantList[i].StakeAddress ||
					gotList[i].Amount.Cmp(wantList[i].Amount) != 0 ||
					gotList[i].WeightedAmount.Cmp(wantList[i].WeightedAmount) != 0 {
					t.Fatalf("entry %d mismatch: got %+v, want %+v", i, gotList[i], wantList[i])
				}
			}
		})
	}

	// Malformed row outside addrA's range is ignored for addrA, but surfaces for addrB and releases iterator.
	if err := mem.Put(getLiquidityStakeEntryKey(types.NewHash([]byte{88}), addrB), []byte{0xba, 0xad}); err != nil {
		t.Fatal(err)
	}
	cdb.reset()
	if _, _, _, err := GetLiquidityStakeListByAddress(cdb, addrA); err != nil {
		t.Fatalf("malformed row outside addrA range failed addrA read: %v", err)
	}
	if cdb.released != 1 {
		t.Fatalf("iterator not released: released=%d", cdb.released)
	}

	cdb.reset()
	if _, _, _, err := GetLiquidityStakeListByAddress(cdb, addrB); err == nil {
		t.Fatal("expected error for malformed row inside addrB range, got nil")
	}
	if cdb.released != 1 {
		t.Fatalf("iterator not released on decode error: released=%d", cdb.released)
	}

	cdb.reset()
	cdb.iterErr = errors.New("simulated liquidity iterator failure")
	if _, _, _, err := GetLiquidityStakeListByAddress(cdb, addrA); !errors.Is(err, cdb.iterErr) {
		t.Fatalf("want iterator error %v, got %v", cdb.iterErr, err)
	}
	if cdb.released != 1 {
		t.Fatalf("iterator not released on iterator error: released=%d", cdb.released)
	}
}

func TestGetAllPillarVotesAndBreakdown_PerEntityPrefixScan(t *testing.T) {
	mem := db.NewMemDB()
	var sharedHashPrefix [31]byte
	for i := range sharedHashPrefix {
		sharedHashPrefix[i] = byte(i + 1)
	}
	idA := makeHashWithLastByte(sharedHashPrefix, 0xaa)
	idB := makeHashWithLastByte(sharedHashPrefix, 0xab) // shares 31-byte prefix with idA
	idC := makeHashWithLastByte(sharedHashPrefix, 0xac)
	idEmpty := makeHashWithLastByte(sharedHashPrefix, 0xff)

	votes := []*PillarVote{
		{Id: idA, Name: "pillar-alpha", Vote: VoteYes},
		{Id: idA, Name: "pillar-beta", Vote: VoteNo},
		{Id: idA, Name: "pillar-gamma", Vote: VoteAbstain},
		{Id: idA, Name: "pillar-delta", Vote: VoteYes},
		{Id: idB, Name: "pillar-alpha", Vote: VoteNo},
		{Id: idB, Name: "pillar-beta", Vote: VoteNo},
		{Id: idC, Name: "pillar-omega", Vote: VoteAbstain},
	}
	for _, v := range votes {
		v.Save(mem)
	}
	// Empty value inside idA's prefix should be skipped.
	emptyVoteKey := (&PillarVote{Id: idA, Name: "pillar-empty"}).Key()
	if err := mem.Put(emptyVoteKey, []byte{}); err != nil {
		t.Fatal(err)
	}

	cdb := &rowCountingDB{DB: mem}

	for _, tc := range []struct {
		name     string
		id       types.Hash
		wantRows int
		wantLen  int
		wantYes  uint32
		wantNo   uint32
	}{
		{name: "idA with 4 votes and 1 empty value", id: idA, wantRows: 5, wantLen: 4, wantYes: 2, wantNo: 1},
		{name: "idB sharing 31-byte prefix", id: idB, wantRows: 2, wantLen: 2, wantYes: 0, wantNo: 2},
		{name: "idC single abstention", id: idC, wantRows: 1, wantLen: 1, wantYes: 0, wantNo: 0},
		{name: "idEmpty no votes", id: idEmpty, wantRows: 0, wantLen: 0, wantYes: 0, wantNo: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantVotes := legacyGetAllPillarVotes(mem, tc.id)

			cdb.reset()
			gotVotes := GetAllPillarVotes(cdb, tc.id)
			if cdb.opened != 1 || cdb.released != 1 {
				t.Fatalf("opened=%d released=%d, want 1 and 1", cdb.opened, cdb.released)
			}
			if cdb.rows != tc.wantRows {
				t.Fatalf("visited %d rows, want %d", cdb.rows, tc.wantRows)
			}
			if len(gotVotes) != tc.wantLen || len(gotVotes) != len(wantVotes) {
				t.Fatalf("len=%d, want %d", len(gotVotes), len(wantVotes))
			}
			for i := range wantVotes {
				if *gotVotes[i] != *wantVotes[i] {
					t.Fatalf("vote %d mismatch: got %+v, want %+v", i, gotVotes[i], wantVotes[i])
				}
			}

			cdb.reset()
			bd := GetVoteBreakdown(cdb, tc.id)
			if cdb.opened != 1 || cdb.released != 1 || cdb.rows != tc.wantRows {
				t.Fatalf("GetVoteBreakdown stats: opened=%d released=%d rows=%d, want 1, 1, %d", cdb.opened, cdb.released, cdb.rows, tc.wantRows)
			}
			if bd.Id != tc.id || bd.Total != uint32(tc.wantLen) || bd.Yes != tc.wantYes || bd.No != tc.wantNo {
				t.Fatalf("breakdown=%+v, want total=%d yes=%d no=%d", bd, tc.wantLen, tc.wantYes, tc.wantNo)
			}
		})
	}

	// Malformed row outside idA's range does not fail idA's read, but panics for idB while still releasing the iterator.
	badVoteKey := (&PillarVote{Id: idB, Name: "pillar-corrupt"}).Key()
	if err := mem.Put(badVoteKey, []byte{0xde, 0xad}); err != nil {
		t.Fatal(err)
	}
	cdb.reset()
	_ = GetAllPillarVotes(cdb, idA)
	if cdb.released != 1 {
		t.Fatalf("iterator not released: released=%d", cdb.released)
	}

	cdb.reset()
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		_ = GetAllPillarVotes(cdb, idB)
	}()
	if !panicked {
		t.Fatal("expected panic on malformed vote inside idB range")
	}
	if cdb.released != 1 {
		t.Fatalf("iterator not released after decode panic: released=%d", cdb.released)
	}

	// Iterator error panics via DealWithErr and still releases iterator.
	cdb.reset()
	cdb.iterErr = errors.New("simulated vote iterator failure")
	panicked = false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		_ = GetAllPillarVotes(cdb, idA)
	}()
	if !panicked {
		t.Fatal("expected panic on iterator error")
	}
	if cdb.released != 1 {
		t.Fatalf("iterator not released after iterator error panic: released=%d", cdb.released)
	}
}
