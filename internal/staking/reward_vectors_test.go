package staking

import (
	"encoding/json"
	"os"
	"testing"
)

type rewardVectors struct {
	Cases   []rewardVector  `json:"cases"`
	Invalid []rewardInvalid `json:"invalid"`
}

type rewardVector struct {
	Name        string        `json:"name"`
	Budget      uint64        `json:"budget"`
	TreasuryBps uint32        `json:"treasury_bps"`
	Epoch       uint64        `json:"epoch"`
	Validators  []rewardInput `json:"validators"`
	Expect      rewardExpect  `json:"expect"`
}

type rewardInvalid struct {
	Name        string `json:"name"`
	Budget      uint64 `json:"budget"`
	TreasuryBps uint32 `json:"treasury_bps"`
	Error       string `json:"error"`
}

type rewardInput struct {
	Address       string            `json:"address"`
	Status        Status            `json:"status"`
	CommissionBps uint32            `json:"commission_bps"`
	Delegations   map[string]uint64 `json:"delegations"`
}

type rewardExpect struct {
	Treasury   uint64                  `json:"treasury"`
	Total      uint64                  `json:"total"`
	Validators map[string]rewardWanted `json:"validators"`
}

type rewardWanted struct {
	Stake      uint64            `json:"stake"`
	Commission uint64            `json:"commission"`
	Total      uint64            `json:"total"`
	Delegators map[string]uint64 `json:"delegators"`
}

func TestRewardVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/rewards.json")
	if err != nil {
		t.Fatal(err)
	}
	var file rewardVectors
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, test := range file.Invalid {
		if _, err := (&State{}).DistributeRewards(test.Budget, test.TreasuryBps); codeOf(err) != test.Error {
			t.Fatalf("%s: want %s, got %v", test.Name, test.Error, err)
		}
	}
	for _, test := range file.Cases {
		state := &State{
			Epoch:       test.Epoch,
			Validators:  map[string]*Validator{},
			Delegations: map[string]map[string]uint64{},
		}
		for _, input := range test.Validators {
			state.Validators[input.Address] = &Validator{
				Address: input.Address, Status: input.Status,
				CommissionBps: input.CommissionBps,
			}
			state.Delegations[input.Address] = input.Delegations
		}
		got, err := state.DistributeRewards(test.Budget, test.TreasuryBps)
		if err != nil {
			t.Fatalf("%s: %v", test.Name, err)
		}
		if got.Treasury != test.Expect.Treasury || got.Total != test.Expect.Total {
			t.Fatalf("%s: treasury/total %d/%d, want %d/%d",
				test.Name, got.Treasury, got.Total, test.Expect.Treasury, test.Expect.Total)
		}
		if len(got.Validators) != len(test.Expect.Validators) {
			t.Fatalf("%s: %d validators, want %d", test.Name, len(got.Validators), len(test.Expect.Validators))
		}
		for _, reward := range got.Validators {
			want, ok := test.Expect.Validators[reward.Validator]
			if !ok {
				t.Fatalf("%s: unexpected validator %s", test.Name, reward.Validator)
			}
			if reward.Stake != want.Stake || reward.Commission != want.Commission || reward.Total != want.Total {
				t.Fatalf("%s %s: %+v, want %+v", test.Name, reward.Validator, reward, want)
			}
			if len(reward.Delegators) != len(want.Delegators) {
				t.Fatalf("%s %s: delegators %+v, want %+v", test.Name, reward.Validator, reward.Delegators, want.Delegators)
			}
			for _, delegator := range reward.Delegators {
				if delegator.Amount != want.Delegators[delegator.Address] {
					t.Fatalf("%s %s: %s got %d, want %d",
						test.Name, reward.Validator, delegator.Address,
						delegator.Amount, want.Delegators[delegator.Address])
				}
			}
		}
		if got.Total != test.Budget {
			t.Fatalf("%s: distributed %d of %d", test.Name, got.Total, test.Budget)
		}
	}
}
