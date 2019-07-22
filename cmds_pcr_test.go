package tpm2

import (
	"bytes"
	"reflect"
	"sort"
	"testing"
)

func TestPCRExtend(t *testing.T) {
	tpm, _ := openTPMSimulatorForTesting(t)
	defer closeTPM(t, tpm)

	for _, data := range []struct {
		desc       string
		index      int
		algorithms []AlgorithmId
		data       []byte
	}{
		{
			desc:       "1",
			index:      0,
			algorithms: []AlgorithmId{AlgorithmSHA1},
			data:       []byte("foo"),
		},
		{
			desc:       "2",
			index:      3,
			algorithms: []AlgorithmId{AlgorithmSHA256},
			data:       []byte("bar"),
		},
		{
			desc:       "3",
			index:      3,
			algorithms: []AlgorithmId{AlgorithmSHA1, AlgorithmSHA256},
			data:       []byte("foo"),
		},
	} {
		t.Run(data.desc, func(t *testing.T) {
			pcrSelection := PCRSelectionList{}
			for _, alg := range data.algorithms {
				pcrSelection = append(pcrSelection,
					PCRSelection{Hash: alg, Select: PCRSelectionData{data.index}})
			}

			origUpdateCounter, _, origValues, err := tpm.PCRRead(pcrSelection)
			if err != nil {
				t.Fatalf("PCRRead failed: %v", err)
			}

			hashList := TaggedHashList{}

			for _, alg := range data.algorithms {
				hasher := cryptHashAlgToGoConstructor(alg)()
				hasher.Write(data.data)
				hashList = append(hashList, TaggedHash{HashAlg: alg, Digest: hasher.Sum(nil)})
			}

			if err := tpm.PCRExtend(Handle(data.index), hashList, nil); err != nil {
				t.Fatalf("PCRExtend failed: %v", err)
			}

			newUpdateCounter, _, newValues, err := tpm.PCRRead(pcrSelection)
			if err != nil {
				t.Fatalf("PCRRead failed: %v", err)
			}

			expectedUpdateCounter := origUpdateCounter + uint32(len(data.algorithms))
			if newUpdateCounter != expectedUpdateCounter {
				t.Errorf("Unexpected update count (got %d, expected %d)", newUpdateCounter,
					expectedUpdateCounter)
			}

			for i, alg := range data.algorithms {
				hasher := cryptHashAlgToGoConstructor(alg)()
				hasher.Write(origValues[i])
				hasher.Write(hashList[i].Digest)

				expected := hasher.Sum(nil)

				if !bytes.Equal(expected, newValues[i]) {
					t.Errorf("Updated PCR has unexpected value for algorithm %v (got %x, "+
						"expected %x)", alg, newValues[i], expected)
				}
			}
		})
	}
}

func TestPCREvent(t *testing.T) {
	tpm, _ := openTPMSimulatorForTesting(t)
	defer closeTPM(t, tpm)

	for _, data := range []struct {
		desc  string
		index int
		data  Event
	}{
		{
			desc:  "1",
			index: 2,
			data:  Event("foo"),
		},
		{
			desc:  "2",
			index: 2,
			data:  Event("bar"),
		},
		{
			desc:  "3",
			index: 5,
			data:  Event("foo"),
		},
	} {
		t.Run(data.desc, func(t *testing.T) {
			pcrSelection := PCRSelectionList{
				PCRSelection{Hash: AlgorithmSHA1, Select: PCRSelectionData{data.index}},
				PCRSelection{Hash: AlgorithmSHA256, Select: PCRSelectionData{data.index}}}

			origUpdateCounter, _, origValues, err := tpm.PCRRead(pcrSelection)
			if err != nil {
				t.Fatalf("PCRRead failed: %v", err)
			}

			digests, err := tpm.PCREvent(Handle(data.index), data.data, nil)
			if err != nil {
				t.Fatalf("PCREvent failed: %v", err)
			}

			for _, alg := range []AlgorithmId{AlgorithmSHA1, AlgorithmSHA256} {
				hasher := cryptHashAlgToGoConstructor(alg)()
				hasher.Write(data.data)
				expectedDigest := hasher.Sum(nil)
				digest := []byte{}
				for _, d := range digests {
					if d.HashAlg == alg {
						digest = d.Digest
						break
					}
				}
				if !bytes.Equal(digest, expectedDigest) {
					t.Errorf("PCREvent returned an unexpected digest for algorithm %v "+
						"(got %x, expected %x)", alg, digest, expectedDigest)
				}
			}

			newUpdateCounter, _, newValues, err := tpm.PCRRead(pcrSelection)
			if err != nil {
				t.Fatalf("PCRRead failed: %v", err)
			}

			expectedUpdateCounter := origUpdateCounter + uint32(len(digests))
			if newUpdateCounter != expectedUpdateCounter {
				t.Errorf("Unexpected update count (got %d, expected %d)", newUpdateCounter,
					expectedUpdateCounter)
			}

			for i, alg := range []AlgorithmId{AlgorithmSHA1, AlgorithmSHA256} {
				hasher := cryptHashAlgToGoConstructor(alg)()
				hasher.Write(origValues[i])
				for _, d := range digests {
					if d.HashAlg == alg {
						hasher.Write(d.Digest)
						break
					}
				}

				expected := hasher.Sum(nil)

				if !bytes.Equal(expected, newValues[i]) {
					t.Errorf("Updated PCR has unexpected value for algorithm %v (got %x, "+
						"expected %x)", alg, newValues[i], expected)
				}
			}
		})
	}
}

func TestPCRRead(t *testing.T) {
	tpm, tcti := openTPMSimulatorForTesting(t)
	defer closeTPM(t, tpm)

	resetTPMSimulator(t, tpm, tcti)

	expectedDigests := make(map[int]map[AlgorithmId][]byte)

	for _, data := range []struct {
		index int
		data  []byte
	}{
		{
			index: 1,
			data:  []byte("foo"),
		},
		{
			index: 2,
			data:  []byte("bar"),
		},
		{
			index: 3,
			data:  []byte("xyz"),
		},
	} {
		_, err := tpm.PCREvent(Handle(data.index), data.data, nil)
		if err != nil {
			t.Fatalf("PCREvent failed: %v", err)
		}
		if _, exists := expectedDigests[data.index]; !exists {
			expectedDigests[data.index] = make(map[AlgorithmId][]byte)
		}
		for _, alg := range []AlgorithmId{AlgorithmSHA1, AlgorithmSHA256} {
			digestSize, _ := cryptGetDigestSize(alg)

			hasher := cryptHashAlgToGoConstructor(alg)()
			hasher.Write(data.data)
			dataDigest := hasher.Sum(nil)

			hasher = cryptHashAlgToGoConstructor(alg)()
			hasher.Write(make([]byte, digestSize))
			hasher.Write(dataDigest)
			expectedDigests[data.index][alg] = hasher.Sum(nil)
		}
	}

	for _, data := range []struct {
		desc      string
		selection PCRSelectionList
	}{
		{
			desc: "SinglePCRSingleBank",
			selection: PCRSelectionList{
				PCRSelection{Hash: AlgorithmSHA256, Select: PCRSelectionData{1}}},
		},
		{
			desc: "MultiplePCRSingleBank",
			selection: PCRSelectionList{
				PCRSelection{Hash: AlgorithmSHA1, Select: PCRSelectionData{2, 3, 1}}},
		},
		{
			desc: "SinglePCRMultipleBank",
			selection: PCRSelectionList{
				PCRSelection{Hash: AlgorithmSHA1, Select: PCRSelectionData{2}},
				PCRSelection{Hash: AlgorithmSHA256, Select: PCRSelectionData{2}}},
		},
		{
			desc: "SinglePCRMultipleBank2",
			selection: PCRSelectionList{
				PCRSelection{Hash: AlgorithmSHA256, Select: PCRSelectionData{2}},
				PCRSelection{Hash: AlgorithmSHA1, Select: PCRSelectionData{2}}},
		},
		{
			desc: "MultiplePCRMultipleBank",
			selection: PCRSelectionList{
				PCRSelection{Hash: AlgorithmSHA1, Select: PCRSelectionData{1, 2, 3}},
				PCRSelection{Hash: AlgorithmSHA256, Select: PCRSelectionData{1, 2, 3}}},
		},
	} {
		t.Run(data.desc, func(t *testing.T) {
			_, pcrSelection, digests, err := tpm.PCRRead(data.selection)
			if err != nil {
				t.Fatalf("PCRRead failed: %v", err)
			}
			for _, s := range data.selection {
				sort.Ints(s.Select)
			}
			if !reflect.DeepEqual(pcrSelection, data.selection) {
				t.Errorf("PCRRead returned an unexpected PCRSelectionList")
			}
			j := 0
			for _, selection := range pcrSelection {
				for _, i := range selection.Select {
					if !bytes.Equal(expectedDigests[i][selection.Hash], digests[j]) {
						t.Errorf("Unexpected digest (got %x, expected %x)", digests[j],
							expectedDigests[i][selection.Hash])
					}
					j++
				}
			}
		})
	}
}