package tpm2

import (
	"testing"
)

func TestGetCapabilityAlgs(t *testing.T) {
	tpm := openTPMForTesting(t)
	defer tpm.Close()

	data, err := tpm.GetCapabilityAlgs(AlgorithmFirst, CapabilityMaxAlgs)
	if err != nil {
		t.Fatalf("GetCapability failed: %v", err)
	}

	if len(data) == 0 {
		t.Errorf("algorithm property list is empty")
	}

	count := 0
	expected := 16

	for _, prop := range data {
		var a AlgorithmAttributes
		switch prop.Alg {
		case AlgorithmRSA:
			a = AttrAsymmetric | AttrObject
		case AlgorithmSHA1:
			a = AttrHash
		case AlgorithmHMAC:
			a = AttrHash | AttrSigning
		case AlgorithmAES:
			a = AttrSymmetric
		case AlgorithmKeyedHash:
			a = AttrHash | AttrEncrypting | AttrSigning | AttrObject
		case AlgorithmXOR:
			a = AttrHash | AttrSymmetric
		case AlgorithmSHA256:
			a = AttrHash
		case AlgorithmRSASSA:
			a = AttrAsymmetric | AttrSigning
		case AlgorithmRSAES:
			a = AttrAsymmetric | AttrEncrypting
		case AlgorithmRSAPSS:
			a = AttrAsymmetric | AttrSigning
		case AlgorithmOAEP:
			a = AttrAsymmetric | AttrEncrypting
		case AlgorithmECDSA:
			a = AttrAsymmetric | AttrSigning | AttrMethod
		case AlgorithmECDH:
			a = AttrAsymmetric | AttrMethod
		case AlgorithmECDAA:
			a = AttrAsymmetric | AttrSigning
		case AlgorithmECC:
			a = AttrAsymmetric | AttrObject
		case AlgorithmSymCipher:
			a = AttrObject
		default:
			continue
		}
		if a != prop.Properties {
			t.Errorf("Unexpected attributes for algorithm %v (got %v, expected %v)",
				prop.Alg, prop.Properties, a)
		}
		count++
	}

	if count < expected {
		t.Errorf("GetCapability didn't return attributes for all of the algorithms expected")
	}
}

func TestGetCapabilityCommands(t *testing.T) {
	tpm := openTPMForTesting(t)
	defer tpm.Close()

	data, err := tpm.GetCapabilityCommands(CommandFirst, CapabilityMaxCommands)
	if err != nil {
		t.Fatalf("GetCapability failed: %v", err)
	}

	if len(data) == 0 {
		t.Errorf("command attribute list is empty")
	}
}

func TestGetCapabilityHandles(t *testing.T) {
	tpm := openTPMForTesting(t)
	defer tpm.Close()

	data, err := tpm.GetCapabilityHandles(HandleTypePermanent, CapabilityMaxHandles)
	if err != nil {
		t.Fatalf("GetCapability failed: %v", err)
	}

	if len(data) == 0 {
		t.Errorf("command attribute list is empty")
	}

	checkIsInList := func(i Handle) {
		for _, h := range data {
			if h == i {
				return
			}
		}
		t.Errorf("Handle 0x%08x not in list of permanent handles", i)
	}

	checkIsInList(HandleOwner)
	checkIsInList(HandleNull)
	checkIsInList(HandlePW)
	checkIsInList(HandleLockout)
	checkIsInList(HandleEndorsement)
	checkIsInList(HandlePlatform)
	checkIsInList(HandlePlatformNV)
}

func TestGetCapabilityPCRs(t *testing.T) {
	tpm := openTPMForTesting(t)
	defer tpm.Close()

	data, err := tpm.GetCapabilityPCRs()
	if err != nil {
		t.Fatalf("GetCapability failed: %v", err)
	}

	if len(data) == 0 {
		t.Errorf("command attribute list is empty")
	}
}

func TestGetCapabilityTPMProperties(t *testing.T) {
	tpm := openTPMForTesting(t)
	defer tpm.Close()

	data, err := tpm.GetCapabilityTPMProperties(PropertyFixed, CapabilityMaxTPMProperties)
	if err != nil {
		t.Fatalf("GetCapability failed: %v", err)
	}

	if len(data) == 0 {
		t.Errorf("TPM property list is empty")
	}

	count := 0
	expected := 4

	// Check a few properties
	for _, prop := range data {
		var val uint32
		switch prop.Property {
		case PropertyLevel:
			val = 0
		case PropertyPCRCount:
			val = 24
		case PropertyPCRSelectMin:
			val = 3
		case PropertyContextHash:
			found := false
			for _, a := range []AlgorithmId{AlgorithmSHA1, AlgorithmSHA256, AlgorithmSHA384,
				AlgorithmSHA512} {
				if uint32(a) == prop.Value {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("GetCapbility returned unexpected value %d for property %v",
					prop.Value, prop.Property)
			}
			count++
			continue
		default:
			continue
		}

		if prop.Value != val {
			t.Errorf("GetCapbility returned unexpected value %d for property %v",
				prop.Value, prop.Property)
		}

		count++
	}

	if count < expected {
		t.Errorf("GetCapability didn't return values for all of the properties expected")
	}
}

func TestGetCapabilityPCRProperties(t *testing.T) {
	tpm := openTPMForTesting(t)
	defer tpm.Close()

	data, err := tpm.GetCapabilityPCRProperties(PropertyPCRFirst, CapabilityMaxPCRProperties)
	if err != nil {
		t.Fatalf("GetCapability failed: %v", err)
	}

	if len(data) == 0 {
		t.Errorf("TPM property list is empty")
	}
}
