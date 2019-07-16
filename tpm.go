package tpm2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
)

const (
	maxCommandSize int = 4096
)

type separatorSentinel struct{}

var Separator separatorSentinel

type TPM interface {
	Close() error

	RunCommandBytes(tag StructTag, commandCode CommandCode, in []byte) (ResponseCode, StructTag, []byte,
		error)
	RunCommandAndReturnRawResponse(commandCode CommandCode, params ...interface{}) (ResponseCode, StructTag, []byte, error)
	RunCommand(commandCode CommandCode, params ...interface{}) error

	WrapHandle(handle Handle) (ResourceContext, error)

	// Start-up
	Startup(startupType StartupType) error
	Shutdown(shutdownType StartupType) error

	// Testing
	SelfTest(fullTest bool) error
	IncrementalSelfTest(toTest AlgorithmList) (AlgorithmList, error)
	GetTestResult() (MaxBuffer, ResponseCode, error)

	// Session Commands
	StartAuthSession(tpmKey, bind ResourceContext, sessionType SessionType, symmetric *SymDef,
		authHash AlgorithmId, auth interface{}) (ResourceContext, error)

	// Object Commands
	Create(parentHandle ResourceContext, inSensitive *SensitiveCreate, inPublic *Public, outsideInfo Data,
		creationPCR PCRSelectionList, session interface{}) (Private, *Public, *CreationData, Digest,
		*TkCreation, error)
	Load(parentHandle ResourceContext, inPrivate Private, inPublic *Public,
		session interface{}) (ResourceContext, Name, error)
	LoadExternal(inPrivate *Sensitive, inPublic *Public, hierarchy Handle) (ResourceContext, Name, error)
	ReadPublic(objectHandle ResourceContext) (*Public, Name, Name, error)

	// Hierarchy Commands
	CreatePrimary(primaryObject Handle, inSensitive *SensitiveCreate, inPublic *Public, outsideInfo Data,
		creationPCR PCRSelectionList, session interface{}) (ResourceContext, *Public, *CreationData,
		Digest, *TkCreation, Name, error)
	Clear(authHandle Handle, session interface{}) error
	ClearControl(authHandle Handle, disable bool, session interface{}) error
	HierarchyChangeAuth(authHandle Handle, newAuth Auth, session interface{}) error

	// Context Management
	FlushContext(flushHandle ResourceContext) error
	EvictControl(auth Handle, objectHandle ResourceContext, persistentHandle Handle,
		session interface{}) (ResourceContext, error)

	// Capability Commands
	GetCapability(capability Capability, property, propertyCount uint32) (*CapabilityData, error)
	GetCapabilityAlgs(first AlgorithmId, propertyCount uint32) (AlgorithmPropertyList, error)
	GetCapabilityCommands(first CommandCode, propertyCount uint32) (CommandAttributesList, error)
	GetCapabilityPPCommands(first CommandCode, propertyCount uint32) (CommandCodeList, error)
	GetCapabilityAuditCommands(first CommandCode, propertyCount uint32) (CommandCodeList, error)
	GetCapabilityHandles(handleType Handle, propertyCount uint32) (HandleList, error)
	GetCapabilityPCRs() (PCRSelectionList, error)
	GetCapabilityTPMProperties(first Property, propertyCount uint32) (TaggedTPMPropertyList, error)
	GetCapabilityPCRProperties(first PropertyPCR, propertyCount uint32) (TaggedPCRPropertyList, error)
	GetCapabilityECCCurves() (ECCCurveList, error)
	GetCapabilityAuthPolicies(first Handle, propertyCount uint32) (TaggedPolicyList, error)

	// Non-volatile Storage
	NVReadPublic(nvIndex ResourceContext) (*NVPublic, Name, error)
}

func concat(chunks ...[]byte) []byte {
	return bytes.Join(chunks, nil)
}

func wrapMarshallingError(err error) error {
	return MarshallingError{err: err}
}

func wrapUnmarshallingError(err error) error {
	return UnmarshallingError{err: err}
}

type commandHeader struct {
	Tag         StructTag
	CommandSize uint32
	CommandCode CommandCode
}

type responseHeader struct {
	Tag          StructTag
	ResponseSize uint32
	ResponseCode ResponseCode
}

func ProcessResponse(commandCode CommandCode, responseCode ResponseCode, responseTag StructTag, response []byte,
	params ...interface{}) error {
	responseHandles := make([]interface{}, 0, len(params))
	responseParams := make([]interface{}, 0, len(params))
	sessionParams := make([]interface{}, 0, len(params))

	sentinels := 0
	for _, param := range params {
		if param == Separator {
			sentinels++
			continue
		}

		switch sentinels {
		case 0:
			_, isHandle := param.(*Handle)
			if !isHandle {
				return InvalidParamError{fmt.Sprintf("invalid response handle type %s",
					reflect.TypeOf(param))}
			}
			responseHandles = append(responseHandles, param)
		case 1:
			responseParams = append(responseParams, param)
		case 2:
			sessionParams = append(sessionParams, param)
		}
	}

	buf := bytes.NewReader(response)

	if len(responseHandles) > 0 {
		if err := UnmarshalFromReader(buf, responseHandles...); err != nil {
			return wrapUnmarshallingError(err)
		}
	}

	rpBuf := buf
	var rpBytes []byte

	if responseTag == TagSessions {
		var parameterSize uint32
		if err := UnmarshalFromReader(buf, &parameterSize); err != nil {
			return wrapUnmarshallingError(err)
		}
		rpBytes = make([]byte, parameterSize)
		n, err := buf.Read(rpBytes)
		if err != nil {
			return wrapUnmarshallingError(fmt.Errorf("error reading response params: %v", err))
		}
		if n < int(parameterSize) {
			return wrapUnmarshallingError(
				errors.New("error reading response parmams: insufficient data"))
		}
		rpBuf = bytes.NewReader(rpBytes)
	}

	if len(responseParams) > 0 {
		if err := UnmarshalFromReader(rpBuf, responseParams...); err != nil {
			return wrapUnmarshallingError(err)
		}
	}

	if responseTag == TagSessions {
		authArea := make([]authResponse, len(sessionParams))
		if err := UnmarshalFromReader(buf, RawSlice(authArea)); err != nil {
			return wrapUnmarshallingError(err)
		}
		if err := processAuthResponseArea(responseCode, commandCode, rpBytes, authArea,
			sessionParams...); err != nil {
			return err
		}
	}

	return nil
}

type tpmImpl struct {
	tpm       io.ReadWriteCloser
	resources map[Handle]ResourceContext
}

func (t *tpmImpl) Close() error {
	if err := t.tpm.Close(); err != nil {
		return err
	}

	for _, rc := range t.resources {
		rc.(resourceContextPrivate).SetTpm(nil)
	}

	return nil
}

func (t *tpmImpl) RunCommandBytes(tag StructTag, commandCode CommandCode, commandBytes []byte) (ResponseCode,
	StructTag, []byte, error) {
	cHeader := commandHeader{tag, 0, commandCode}
	cHeader.CommandSize = uint32(binary.Size(cHeader) + len(commandBytes))

	headerBytes, err := MarshalToBytes(cHeader)
	if err != nil {
		return 0, 0, nil, wrapMarshallingError(err)
	}

	if _, err := t.tpm.Write(concat(headerBytes, commandBytes)); err != nil {
		return 0, 0, nil, TPMWriteError{IOError: err}
	}

	responseBytes := make([]byte, maxCommandSize)
	responseLen, err := t.tpm.Read(responseBytes)
	if err != nil {
		return 0, 0, nil, TPMReadError{IOError: err}
	}
	responseBytes = responseBytes[:responseLen]

	var rHeader responseHeader
	rHeaderLen, err := UnmarshalFromBytes(responseBytes, &rHeader)
	if err != nil {
		return 0, 0, nil, wrapUnmarshallingError(err)
	}

	responseBytes = responseBytes[rHeaderLen:]

	if err := DecodeResponseCode(rHeader.ResponseCode); err != nil {
		return rHeader.ResponseCode, rHeader.Tag, nil, err
	}

	return rHeader.ResponseCode, rHeader.Tag, responseBytes, nil
}

func (t *tpmImpl) RunCommandAndReturnRawResponse(commandCode CommandCode, params ...interface{}) (ResponseCode,
	StructTag, []byte, error) {
	commandHandles := make([]interface{}, 0, len(params))
	commandHandleNames := make([]Name, 0, len(params))
	commandParams := make([]interface{}, 0, len(params))
	sessionParams := make([]interface{}, 0, len(params))

	sentinels := 0
	for _, param := range params {
		if param == Separator {
			sentinels++
			continue
		}

		switch sentinels {
		case 0:
			rc, isRc := param.(ResourceContext)
			if !isRc {
				handle, isHandle := param.(Handle)
				if !isHandle {
					return 0, 0, nil, InvalidParamError{
						fmt.Sprintf("invalid command handle type %s",
							reflect.TypeOf(param))}
				}
				commandHandles = append(commandHandles, param)
				commandHandleNames = append(commandHandleNames,
					(&permanentContext{handle: handle}).Name())
			} else {
				commandHandles = append(commandHandles, rc.Handle())
				commandHandleNames = append(commandHandleNames, rc.Name())
			}
		case 1:
			commandParams = append(commandParams, param)
		case 2:
			sessionParams = append(sessionParams, param)
		}
	}

	var chBytes []byte
	var cpBytes []byte
	var caBytes []byte

	var err error

	if len(commandHandles) > 0 {
		chBytes, err = MarshalToBytes(commandHandles...)
		if err != nil {
			return 0, 0, nil, wrapMarshallingError(err)
		}
	}

	if len(commandParams) > 0 {
		cpBytes, err = MarshalToBytes(commandParams...)
		if err != nil {
			return 0, 0, nil, wrapMarshallingError(err)
		}
	}

	tag := TagNoSessions
	if len(sessionParams) > 0 {
		tag = TagSessions
		authArea, err := buildCommandAuthArea(commandCode, commandHandleNames, cpBytes, sessionParams...)
		if err != nil {
			return 0, 0, nil, err
		}
		caBytes, err = MarshalToBytes(&authArea)
		if err != nil {
			return 0, 0, nil, wrapMarshallingError(err)
		}
	}

	responseCode, responseTag, responseBytes, err :=
		t.RunCommandBytes(tag, commandCode, concat(chBytes, caBytes, cpBytes))
	if err != nil {
		return 0, 0, nil, err
	}

	return responseCode, responseTag, responseBytes, nil
}

func (t *tpmImpl) RunCommand(commandCode CommandCode, params ...interface{}) error {
	commandArgs := make([]interface{}, 0, len(params))
	responseArgs := make([]interface{}, 0, len(params))

	sentinels := 0
	for _, param := range params {
		switch sentinels {
		case 0, 1:
			commandArgs = append(commandArgs, param)
			if param == Separator {
				sentinels++
			}
		case 2, 3:
			responseArgs = append(responseArgs, param)
			if param == Separator {
				sentinels++
			}
		case 4:
			commandArgs = append(commandArgs, param)
			responseArgs = append(responseArgs, param)
			if param == Separator {
				sentinels++
			}
		}
	}

	responseCode, responseTag, responseBytes, err :=
		t.RunCommandAndReturnRawResponse(commandCode, commandArgs...)
	if err != nil {
		return err
	}

	return ProcessResponse(commandCode, responseCode, responseTag, responseBytes, responseArgs...)
}

func newTPMImpl(t io.ReadWriteCloser) *tpmImpl {
	r := new(tpmImpl)
	r.tpm = t
	r.resources = make(map[Handle]ResourceContext)

	return r
}

func OpenTPM(path string) (TPM, error) {
	tpm, err := openLinuxTPMDevice(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open TPM: %v", err)
	}

	return newTPMImpl(tpm), nil
}
