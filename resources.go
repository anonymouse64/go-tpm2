// Copyright 2019 Canonical Ltd.
// Licensed under the LGPLv3 with static-linking exception.
// See LICENCE file for details.

package tpm2

import (
	"bytes"
	"crypto"
	_ "crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/canonical/go-tpm2/mu"

	"golang.org/x/xerrors"
)

// HandleContext corresponds to an entity that resides on the TPM. Implementations of HandleContext maintain some host-side
// state in order to be able to participate in HMAC sessions. They are invalidated when used in a command that results in the
// entity being flushed or evicted from the TPM. Once invalidated, they can no longer be used.
type HandleContext interface {
	// Handle returns the handle of the corresponding entity on the TPM. If the HandleContext has been invalidated then this will
	// return HandleUnassigned.
	Handle() Handle
	Name() Name                        // The name of the entity
	SerializeToBytes() []byte          // Return a byte slice containing the serialized form of this HandleContext
	SerializeToWriter(io.Writer) error // Write the serialized form of this HandleContext to the supplied io.Writer
}

type handleContextPrivate interface {
	invalidate()
}

// SessionAttributes is a set of flags that specify the usage and behaviour of a session.
type SessionAttributes int

const (
	// AttrContinueSession specifies that the session should not be flushed from the TPM after it is used. If a session is used without
	// this flag, it will be flushed from the TPM after the command completes. In this case, the HandleContext associated with the
	// session will be invalidated.
	AttrContinueSession SessionAttributes = 1 << iota

	// AttrAuditExclusive indicates that the session should be used for auditing and that the command should only be executed if the
	// session is exclusive at the start of the command. A session becomes exclusive when it is used for auditing for the first time,
	// or if the AttrAuditReset attribute is provided. A session will remain exclusive until the TPM executes any command where the
	// exclusive session isn't used for auditing, if that command allows for audit sessions to be provided.
	AttrAuditExclusive

	// AttrAuditReset indicates that the session should be used for auditing and that the audit digest of the session should be reset.
	// The session will subsequently become exclusive. A session will remain exclusive until the TPM executes any command where the
	// exclusive session isn't used for auditing, if that command allows for audit sessions to be provided.
	AttrAuditReset

	// AttrCommandEncrypt specifies that the session should be used for encryption of the first command parameter before being sent
	// from the host to the TPM. This can only be used for parameters that have types corresponding to TPM2B prefixed TCG types,
	// and requires a session that was configured with a valid symmetric algorithm via the symmetric argument of
	// TPMContext.StartAuthSession.
	AttrCommandEncrypt

	// AttrResponseEncrypt specifies that the session should be used for encryption of the first response parameter before being sent
	// from the TPM to the host. This can only be used for parameters that have types corresponding to TPM2B prefixed TCG types, and
	// requires a session that was configured with a valid symmetric algorithm via the symmetric argument of TPMContext.StartAuthSession.
	// This package automatically decrypts the received encrypted response parameter.
	AttrResponseEncrypt

	// AttrAudit indicates that the session should be used for auditing. If this is the first time that the session is used for auditing,
	// then this attribute will result in the session becoming exclusive. A session will remain exclusive until the TPM executes any
	// command where the exclusive session isn't used for auditing, if that command allows for audit sessions to be provided.
	AttrAudit
)

// SessionContext is a HandleContext that corresponds to a session on the TPM.
type SessionContext interface {
	HandleContext
	NonceTPM() Nonce   // The most recent TPM nonce value
	IsAudit() bool     // Whether the session has been used for audit
	IsExclusive() bool // Whether the most recent response from the TPM indicated that the session is exclusive for audit purposes

	SetAttrs(attrs SessionAttributes)                 // Set the attributes that will be used for this SessionContext
	WithAttrs(attrs SessionAttributes) SessionContext // Return a duplicate of this SessionContext with the specified attributes

	// IncludeAttrs returns a duplicate of this SessionContext and its attributes with the specified attributes included.
	IncludeAttrs(attrs SessionAttributes) SessionContext
	// ExcludeAttrs returns a duplicate of this SessionContext and its attributes with the specified attributes excluded.
	ExcludeAttrs(attrs SessionAttributes) SessionContext
}

// ResourceContext is a HandleContext that corresponds to a non-session entity on the TPM.
type ResourceContext interface {
	HandleContext

	// SetAuthValue sets the authorization value that will be used in authorization roles where knowledge of the authorization
	// value is required. Functions that create resources on the TPM and return a ResourceContext will set this automatically,
	// else it will need to be set manually.
	SetAuthValue([]byte)
}

type resourceContextPrivate interface {
	GetAuthValue() []byte
}

type handleContextType uint8

const (
	handleContextTypeDummy handleContextType = iota
	handleContextTypePermanent
	handleContextTypeObject
	handleContextTypeNvIndex
	handleContextTypeSession
)

type sessionContextData struct {
	IsAudit        bool
	IsExclusive    bool
	HashAlg        HashAlgorithmId
	SessionType    SessionType
	PolicyHMACType policyHMACType
	IsBound        bool
	BoundEntity    Name
	SessionKey     []byte
	NonceCaller    Nonce
	NonceTPM       Nonce
	Symmetric      *SymDef
}

type handleContextU struct {
	Object  *Public
	NV      *NVPublic
	Session *sessionContextData
}

func (d *handleContextU) Select(selector reflect.Value) interface{} {
	switch selector.Interface().(handleContextType) {
	case handleContextTypeDummy, handleContextTypePermanent:
		return mu.NilUnionValue
	case handleContextTypeObject:
		return &d.Object
	case handleContextTypeNvIndex:
		return &d.NV
	case handleContextTypeSession:
		return &d.Session
	default:
		return nil
	}
}

type handleContext struct {
	Type handleContextType
	H    Handle
	N    Name
	Data *handleContextU `tpm2:"selector:Type"`
}

func (h *handleContext) Handle() Handle {
	return h.H
}

func (h *handleContext) Name() Name {
	return h.N
}

func (h *handleContext) SerializeToBytes() []byte {
	data, err := mu.MarshalToBytes(h)
	if err != nil {
		panic(fmt.Sprintf("cannot marshal context data: %v", err))
	}
	hash := crypto.SHA256.New()
	hash.Write(data)
	data, err = mu.MarshalToBytes(HashAlgorithmSHA256, hash.Sum(nil), data)
	if err != nil {
		panic(fmt.Sprintf("cannot pack context blob and checksum: %v", err))
	}
	return data
}

func (h *handleContext) SerializeToWriter(w io.Writer) error {
	data, err := mu.MarshalToBytes(h)
	if err != nil {
		panic(fmt.Sprintf("cannot marshal context data: %v", err))
	}
	hash := crypto.SHA256.New()
	hash.Write(data)
	_, err = mu.MarshalToWriter(w, HashAlgorithmSHA256, hash.Sum(nil), data)
	return err
}

func (h *handleContext) invalidate() {
	h.H = HandleUnassigned
	h.N = make(Name, binary.Size(Handle(0)))
	binary.BigEndian.PutUint32(h.N, uint32(h.H))
}

func (h *handleContext) checkConsistency() error {
	switch h.Type {
	case handleContextTypePermanent:
		switch h.Handle().Type() {
		case HandleTypePCR, HandleTypePermanent:
		default:
			return errors.New("inconsistent handle type for permanent context")
		}
		if !h.Name().IsHandle() || h.Name().Handle() != h.Handle() {
			return errors.New("name inconsistent with handle for permanent context")
		}
	case handleContextTypeObject:
		switch h.Handle().Type() {
		case HandleTypeTransient, HandleTypePersistent:
		default:
			return errors.New("inconsistent handle type for object context")
		}
		if h.Data.Object == nil {
			return errors.New("no public area for object context")
		}
		if !h.Data.Object.compareName(h.Name()) {
			return errors.New("name inconsistent with public area for object context")
		}
	case handleContextTypeNvIndex:
		if h.Handle().Type() != HandleTypeNVIndex {
			return errors.New("inconsistent handle type for NV context")
		}
		if h.Data.NV == nil {
			return errors.New("no public area for NV context")
		}
		if !h.Data.NV.compareName(h.Name()) {
			return errors.New("name inconsistent with public area for NV context")
		}
	case handleContextTypeSession:
		switch h.Handle().Type() {
		case HandleTypeHMACSession, HandleTypePolicySession:
		default:
			return errors.New("inconsistent handle type for session context")
		}
		if !h.Name().IsHandle() || h.Name().Handle() != h.Handle() {
			return errors.New("name inconsistent with handle for session context")
		}
		scData := h.Data.Session
		if scData != nil {
			if !scData.IsAudit && scData.IsExclusive {
				return errors.New("inconsistent audit attributes for session context")
			}
			if !scData.HashAlg.Supported() {
				return errors.New("invalid digest algorithm for session context")
			}
			switch scData.SessionType {
			case SessionTypeHMAC, SessionTypePolicy, SessionTypeTrial:
			default:
				return errors.New("invalid session type for session context")
			}
			if scData.PolicyHMACType > policyHMACTypeMax {
				return errors.New("invalid policy session HMAC type for session context")
			}
			if (scData.IsBound && len(scData.BoundEntity) == 0) || (!scData.IsBound && len(scData.BoundEntity) > 0) {
				return errors.New("invalid bind properties for session context")
			}
			digestSize := scData.HashAlg.Size()
			if len(scData.SessionKey) != digestSize && len(scData.SessionKey) != 0 {
				return errors.New("unexpected session key size for session context")
			}
			if len(scData.NonceCaller) != digestSize || len(scData.NonceTPM) != digestSize {
				return errors.New("unexpected nonce size for session context")
			}
			switch scData.Symmetric.Algorithm {
			case SymAlgorithmAES, SymAlgorithmXOR, SymAlgorithmNull, SymAlgorithmSM4, SymAlgorithmCamellia:
			default:
				return errors.New("invalid symmetric algorithm for session context")
			}
			switch scData.Symmetric.Algorithm {
			case SymAlgorithmAES, SymAlgorithmSM4, SymAlgorithmCamellia:
				if scData.Symmetric.Mode.Sym != SymModeCFB {
					return errors.New("invalid symmetric mode for session context")
				}
			}
		}
	default:
		return errors.New("unrecognized context type")
	}
	return nil
}

type dummyContext struct {
	handleContext
}

func (r *dummyContext) SerializeToBytes() []byte {
	return nil
}

func (r *dummyContext) SerializeToWriter(io.Writer) error {
	return nil
}

func (r *dummyContext) SetAuthValue([]byte) {}

func (r *dummyContext) invalidate() {}

func makeDummyContext(handle Handle) *dummyContext {
	name := make(Name, binary.Size(Handle(0)))
	binary.BigEndian.PutUint32(name, uint32(handle))
	return &dummyContext{
		handleContext: handleContext{
			Type: handleContextTypeDummy,
			H:    handle,
			N:    name}}
}

type resourceContext struct {
	handleContext
	authValue []byte
}

func (r *resourceContext) SetAuthValue(authValue []byte) {
	r.authValue = authValue
}

func (r *resourceContext) GetAuthValue() []byte {
	return r.authValue
}

type permanentContext struct {
	resourceContext
}

func (r *permanentContext) invalidate() {}

func makePermanentContext(handle Handle) *permanentContext {
	name := make(Name, binary.Size(Handle(0)))
	binary.BigEndian.PutUint32(name, uint32(handle))
	return &permanentContext{
		resourceContext: resourceContext{
			handleContext: handleContext{
				Type: handleContextTypePermanent,
				H:    handle,
				N:    name}}}
}

type objectContext struct {
	resourceContext
}

func (r *objectContext) GetPublic() *Public {
	return r.Data.Object
}

func makeObjectContext(handle Handle, name Name, public *Public) *objectContext {
	return &objectContext{
		resourceContext: resourceContext{
			handleContext: handleContext{
				Type: handleContextTypeObject,
				H:    handle,
				N:    name,
				Data: &handleContextU{Object: public}}}}
}

func (t *TPMContext) makeObjectContextFromTPM(context ResourceContext, sessions ...SessionContext) (ResourceContext, error) {
	pub, name, _, err := t.ReadPublic(context, sessions...)
	if err != nil {
		return nil, err
	}
	if n, err := pub.Name(); err != nil {
		return nil, &InvalidResponseError{CommandReadPublic, fmt.Sprintf("cannot compute name of returned public area: %v", err)}
	} else if !bytes.Equal(n, name) {
		return nil, &InvalidResponseError{CommandReadPublic, "name and public area don't match"}
	}
	return makeObjectContext(context.Handle(), name, pub), nil
}

type nvIndexContext struct {
	resourceContext
}

func (r *nvIndexContext) GetPublic() *NVPublic {
	return r.Data.NV
}

func (r *nvIndexContext) SetAttr(a NVAttributes) {
	r.Data.NV.Attrs |= a
	name, _ := r.Data.NV.Name()
	r.N = name
}

func (r *nvIndexContext) ClearAttr(a NVAttributes) {
	r.Data.NV.Attrs &= ^a
	name, _ := r.Data.NV.Name()
	r.N = name
}

func (r *nvIndexContext) Attrs() NVAttributes {
	return r.Data.NV.Attrs
}

func makeNVIndexContext(name Name, public *NVPublic) *nvIndexContext {
	return &nvIndexContext{
		resourceContext: resourceContext{
			handleContext: handleContext{
				Type: handleContextTypeNvIndex,
				H:    public.Index,
				N:    name,
				Data: &handleContextU{NV: public}}}}
}

func (t *TPMContext) makeNVIndexContextFromTPM(context ResourceContext, sessions ...SessionContext) (ResourceContext, error) {
	pub, name, err := t.NVReadPublic(context, sessions...)
	if err != nil {
		return nil, err
	}
	if n, err := pub.Name(); err != nil {
		return nil, &InvalidResponseError{CommandNVReadPublic, fmt.Sprintf("cannot compute name of returned public area: %v", err)}
	} else if !bytes.Equal(n, name) {
		return nil, &InvalidResponseError{CommandNVReadPublic, "name and public area don't match"}
	}
	if pub.Index != context.Handle() {
		return nil, &InvalidResponseError{CommandNVReadPublic, "unexpected index in public area"}
	}
	return makeNVIndexContext(name, pub), nil
}

type sessionContext struct {
	*handleContext
	attrs SessionAttributes
}

func (r *sessionContext) NonceTPM() Nonce {
	d := r.Data()
	if d == nil {
		return nil
	}
	return d.NonceTPM
}

func (r *sessionContext) IsAudit() bool {
	d := r.Data()
	if d == nil {
		return false
	}
	return d.IsAudit
}

func (r *sessionContext) IsExclusive() bool {
	d := r.Data()
	if d == nil {
		return false
	}
	return d.IsExclusive
}

func (r *sessionContext) SetAttrs(attrs SessionAttributes) {
	r.attrs = attrs
}

func (r *sessionContext) WithAttrs(attrs SessionAttributes) SessionContext {
	return &sessionContext{handleContext: r.handleContext, attrs: attrs}
}

func (r *sessionContext) IncludeAttrs(attrs SessionAttributes) SessionContext {
	return &sessionContext{handleContext: r.handleContext, attrs: r.attrs | attrs}
}

func (r *sessionContext) ExcludeAttrs(attrs SessionAttributes) SessionContext {
	return &sessionContext{handleContext: r.handleContext, attrs: r.attrs &^ attrs}
}

func (r *sessionContext) Data() *sessionContextData {
	return r.handleContext.Data.Session
}

func (r *sessionContext) tpmAttrs() sessionAttrs {
	var attrs sessionAttrs
	if r.attrs&AttrContinueSession > 0 {
		attrs |= attrContinueSession
	}
	if r.attrs&AttrAuditExclusive > 0 {
		attrs |= (attrAuditExclusive | attrAudit)
	}
	if r.attrs&AttrAuditReset > 0 {
		attrs |= (attrAuditReset | attrAudit)
	}
	if r.attrs&AttrCommandEncrypt > 0 {
		attrs |= attrDecrypt
	}
	if r.attrs&AttrResponseEncrypt > 0 {
		attrs |= attrEncrypt
	}
	if r.attrs&AttrAudit > 0 {
		attrs |= attrAudit
	}
	return attrs
}

func makeSessionContext(handle Handle, data *sessionContextData) *sessionContext {
	name := make(Name, binary.Size(Handle(0)))
	binary.BigEndian.PutUint32(name, uint32(handle))
	return &sessionContext{
		handleContext: &handleContext{
			Type: handleContextTypeSession,
			H:    handle,
			N:    name,
			Data: &handleContextU{Session: data}}}
}

// CreateResourceContextFromTPM creates and returns a new ResourceContext for the specified handle. It will execute a command to read
// the public area from the TPM in order to initialize state that is maintained on the host side. A ResourceUnavailableError error
// will be returned if the specified handle references a resource that is currently unavailable. If this function is called without any
// sessions, it does not benefit from any integrity protections other than a consistency cross-check that is performed on the returned
// data to make sure that the name and public area match. Applications should consider the implications of this during subsequent use
// of the ResourceContext. If any sessions are passed then the pubic area is read back from the TPM twice - the session is used only
// on the second read once the name is known. This second read provides an assurance that an entity with the name of the returned
// ResourceContext actually lives on the TPM.
//
// This function will panic if handle doesn't correspond to a NV index, transient object or persistent object.
//
// If subsequent use of the returned ResourceContext requires knowledge of the authorization value of the corresponding TPM resource,
// this should be provided by calling ResourceContext.SetAuthValue.
func (t *TPMContext) CreateResourceContextFromTPM(handle Handle, sessions ...SessionContext) (ResourceContext, error) {
	switch handle.Type() {
	case HandleTypeNVIndex, HandleTypeTransient, HandleTypePersistent:
	default:
		panic("invalid handle type")
	}

	var rc ResourceContext = makeDummyContext(handle)
	var s []SessionContext
	for i := 0; i < 2; i++ {
		var err error
		if handle.Type() == HandleTypeNVIndex {
			rc, err = t.makeNVIndexContextFromTPM(rc, s...)
		} else {
			rc, err = t.makeObjectContextFromTPM(rc, s...)
		}

		switch {
		case IsTPMWarning(err, WarningReferenceH0, AnyCommandCode):
			return nil, ResourceUnavailableError{handle}
		case IsTPMHandleError(err, ErrorHandle, AnyCommandCode, AnyHandleIndex):
			return nil, ResourceUnavailableError{handle}
		case err != nil:
			return nil, err
		}

		if len(sessions) == 0 {
			break
		}
		s = sessions
	}

	return rc, nil
}

// CreateIncompleteSessionContext creates and returns a new SessionContext for the specified handle. The returned SessionContext will
// not be complete and the session associated with it cannot be used in any command other than TPMContext.FlushContext.
//
// This function will panic if handle doesn't correspond to a session.
func CreateIncompleteSessionContext(handle Handle) SessionContext {
	switch handle.Type() {
	case HandleTypeHMACSession, HandleTypePolicySession:
		return makeSessionContext(handle, nil)
	default:
		panic("invalid handle type")
	}
}

// GetPermanentContext returns a ResourceContext for the specified permanent handle or PCR handle.
//
// This function will panic if handle does not correspond to a permanent or PCR handle.
//
// If subsequent use of the returned ResourceContext requires knowledge of the authorization value of the corresponding TPM resource,
// this should be provided by calling ResourceContext.SetAuthValue.
func (t *TPMContext) GetPermanentContext(handle Handle) ResourceContext {
	switch handle.Type() {
	case HandleTypePermanent, HandleTypePCR:
		if rc, exists := t.permanentResources[handle]; exists {
			return rc
		}

		rc := makePermanentContext(handle)
		t.permanentResources[handle] = rc
		return rc
	default:
		panic("invalid handle type")
	}
}

// OwnerHandleContext returns the ResouceContext corresponding to the owner hiearchy.
func (t *TPMContext) OwnerHandleContext() ResourceContext {
	return t.GetPermanentContext(HandleOwner)
}

// NulHandleContext returns the ResourceContext corresponding to the null hiearchy.
func (t *TPMContext) NullHandleContext() ResourceContext {
	return t.GetPermanentContext(HandleNull)
}

// LockoutHandleContext returns the ResourceContext corresponding to the lockout hiearchy.
func (t *TPMContext) LockoutHandleContext() ResourceContext {
	return t.GetPermanentContext(HandleLockout)
}

// EndorsementHandleContext returns the ResourceContext corresponding to the endorsement hiearchy.
func (t *TPMContext) EndorsementHandleContext() ResourceContext {
	return t.GetPermanentContext(HandleEndorsement)
}

// PlatformHandleContext returns the ResourceContext corresponding to the platform hiearchy.
func (t *TPMContext) PlatformHandleContext() ResourceContext {
	return t.GetPermanentContext(HandlePlatform)
}

// PlatformNVHandleContext returns the ResourceContext corresponding to the platform hiearchy.
func (t *TPMContext) PlatformNVHandleContext() ResourceContext {
	return t.GetPermanentContext(HandlePlatformNV)
}

// PCRHandleContext returns the ResourceContext corresponding to the PCR at the specified index. It will panic if pcr is not a valid
// PCR index.
func (t *TPMContext) PCRHandleContext(pcr int) ResourceContext {
	h := Handle(pcr)
	if h.Type() != HandleTypePCR {
		panic("invalid PCR index")
	}
	return t.GetPermanentContext(h)
}

// CreateHandleContextFromReader returns a new HandleContext created from the serialized data read from the supplied io.Reader. This
// should contain data that was previously created by HandleContext.SerializeToBytes or HandleContext.SerializeToWriter.
//
// If the supplied data corresponds to a session then a SessionContext will be returned, else a ResourceContext will be returned.
//
// If a ResourceContext is returned and subsequent use of it requires knowledge of the authorization value of the corresponding TPM
// resource, this should be provided by calling ResourceContext.SetAuthValue.
func CreateHandleContextFromReader(r io.Reader) (HandleContext, error) {
	var integrityAlg HashAlgorithmId
	var integrity []byte
	var b []byte
	if _, err := mu.UnmarshalFromReader(r, &integrityAlg, &integrity, &b); err != nil {
		return nil, xerrors.Errorf("cannot unpack context blob and checksum: %w", err)
	}

	if !integrityAlg.Supported() {
		return nil, errors.New("invalid checksum algorithm")
	}
	h := integrityAlg.NewHash()
	h.Write(b)
	if !bytes.Equal(h.Sum(nil), integrity) {
		return nil, errors.New("invalid checksum")
	}

	var data *handleContext
	n, err := mu.UnmarshalFromBytes(b, &data)
	if err != nil {
		return nil, xerrors.Errorf("cannot unmarshal context data: %w", err)
	}
	if n < len(b) {
		return nil, errors.New("context blob contains trailing bytes")
	}

	if data.Type == handleContextTypePermanent {
		return nil, errors.New("cannot create a permanent context from serialized data")
	}

	if err := data.checkConsistency(); err != nil {
		return nil, err
	}

	var hc HandleContext
	switch data.Type {
	case handleContextTypeObject:
		hc = &objectContext{resourceContext: resourceContext{handleContext: *data}}
	case handleContextTypeNvIndex:
		hc = &nvIndexContext{resourceContext: resourceContext{handleContext: *data}}
	case handleContextTypeSession:
		hc = &sessionContext{handleContext: data}
	default:
		panic("not reached")
	}

	return hc, nil
}

// CreateHandleContextFromBytes returns a new HandleContext created from the serialized data read from the supplied byte slice. This
// should contain data that was previously created by HandleContext.SerializeToBytes or HandleContext.SerializeToWriter.
//
// If the supplied data corresponds to a session then a SessionContext will be returned, else a ResourceContext will be returned.
//
// If a ResourceContext is returned and subsequent use of it requires knowledge of the authorization value of the corresponding TPM
// resource, this should be provided by calling ResourceContext.SetAuthValue.
func CreateHandleContextFromBytes(b []byte) (HandleContext, int, error) {
	buf := bytes.NewReader(b)
	rc, err := CreateHandleContextFromReader(buf)
	if err != nil {
		return nil, 0, err
	}
	return rc, len(b) - buf.Len(), nil
}

// CreateNVIndexResourceContextFromPublic returns a new ResourceContext created from the provided public area. If subsequent use of
// the returned ResourceContext requires knowledge of the authorization value of the corresponding TPM resource, this should be
// provided by calling ResourceContext.SetAuthValue.
func CreateNVIndexResourceContextFromPublic(pub *NVPublic) (ResourceContext, error) {
	name, err := pub.Name()
	if err != nil {
		return nil, fmt.Errorf("cannot compute name from public area: %v", err)
	}
	rc := makeNVIndexContext(name, pub)
	if err := rc.checkConsistency(); err != nil {
		return nil, err
	}
	return rc, nil
}

// CreateObjectResourceContextFromPublic returns a new ResourceContext created from the provided public area. If subsequent use of
// the returned ResourceContext requires knowledge of the authorization value of the corresponding TPM resource, this should be
// provided by calling ResourceContext.SetAuthValue.
func CreateObjectResourceContextFromPublic(handle Handle, pub *Public) (ResourceContext, error) {
	name, err := pub.Name()
	if err != nil {
		return nil, fmt.Errorf("cannot compute name from public area: %v", err)
	}
	rc := makeObjectContext(handle, name, pub)
	if err := rc.checkConsistency(); err != nil {
		return nil, err
	}
	return rc, nil
}
