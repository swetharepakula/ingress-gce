/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package psc

import "testing"

func TestGetConnectionPreference(t *testing.T) {
	testcases := []struct {
		desc         string
		connPref     string
		expectedPref string
		expectErr    bool
	}{
		{
			desc:         "Valid connection preference option",
			connPref:     "acceptAutomatic",
			expectedPref: ConnPreferenceAcceptAutomatic,
			expectErr:    false,
		},
		{
			desc:         "Valid connection preference option with leading whitespace",
			connPref:     " acceptAutomatic",
			expectedPref: ConnPreferenceAcceptAutomatic,
			expectErr:    false,
		},
		{
			desc:         "Valid connection preference option with trailing whitespace",
			connPref:     "acceptAutomatic ",
			expectedPref: ConnPreferenceAcceptAutomatic,
			expectErr:    false,
		},
		{
			desc:         "Invalid connection preference option",
			connPref:     "invalidPreference",
			expectedPref: "",
			expectErr:    true,
		},
	}

	for _, tc := range testcases {

		connPref, err := GetConnectionPreference(tc.connPref)
		if tc.expectErr && err == nil {
			t.Errorf("%s: GetConnectionPreference(%s) should have resulted in an error", tc.desc, tc.connPref)
		} else if !tc.expectErr && err != nil {
			t.Errorf("%s: GetConnectionPreference(%s) resulted in an unexpected error: %s", tc.desc, tc.connPref, err)
		}

		if connPref != tc.expectedPref {
			t.Errorf("%s: Expected GetConnectionPreference(%s) to be %s, but received %s", tc.desc, tc.connPref, tc.expectedPref, connPref)
		}
	}

}
