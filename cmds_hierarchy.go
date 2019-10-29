// Copyright 2019 Canonical Ltd.
// Licensed under the LGPLv3 with static-linking exception.
// See LICENCE file for details.

package tpm2

// Section 24 - Hierarchy Commands

import (
	"fmt"
)

// CreatePrimary executes the TPM2_CreatePrimary command to create a new primary object in the hierarchy corresponding to
// primaryObject.
//
// The primaryObject parameter should correspond to a hierarchy. The command requires authorization with the user auth role for
// primaryObject, provided via primaryObjectAuth.
//
// A template for the object is provided via the inPublic parameter. The Type field of inPublic defines the algorithm for the object.
// The NameAlg field defines the digest algorithm for computing the name of the object. The Attrs field defines the attributes of
// the object. The AuthPolicy field allows an authorization policy to be defined for the new object.
//
// Data that will form part of the sensitive area of the object can be provided via inSensitive, which is optional.
//
// If the Attrs field of inPublic does not have the AttrSensitiveDataOrigin attribute set, then the sensitive data in the created
// object is initialized with the data provided via the Data field of inSensitive.
//
// If the Attrs field of inPublic has the AttrSensitiveDataOrigin attribute set and Type is AlgorithmSymCipher, then the sensitive
// data in the created object is initialized with a TPM generated key. The size of this key is determined by the value of the Params
// field of inPublic. If Type is AlgorithmKeyedHash, then the sensitive data in the created object is initialized with a TPM
// generated value that is the same size as the name algorithm selected by the NameAlg field of inPublic.
//
// If the Type field of inPublic is AlgorithmRSA or AlgorithmECC, then the sensitive data in the created object is initialized with
// a TPM generated private key. The size of this is determined by the value of the Params field of inPublic.
//
// If the Type field of inPublic is AlgorithmKeyedHash and the Attrs field has AttrSensitiveDataOrigin, AttrSign and AttrDecrypt all
// clear, then the created object is a sealed data object.
//
// If the Attrs field of inPublic has the AttrRestricted and AttrDecrypt attributes set, and the Type field is not AlgorithmKeyedHash,
// then the newly created object will be a storage parent.
//
// If the Attrs field of inPublic has the AttrRestricted and AttrDecrypt attributes set, and the Type field is AlgorithmKeyedHash, then
// the newly created object will be a derivation parent.
//
// The authorization value for the created object is initialized to the value of the UserAuth field of inSensitive.
//
// If there are no available slots for new objects on the TPM, a *TPMWarning error with a warning code of WarningObjectMemory will
// be returned.
//
// CreatePrimary performs many of the same checks as TPMContext.Create. For the purpose of checking the public attributes, the primary
// seeds are assumed to have the AttrFixedTPM, AttrFixedParent, AttrDecrypt and AttrRestricted attributes set.
//
// On success, a ResourceContext instance will be returned that corresponds to the newly created object on the TPM. If the Type field
// of inPublic is AlgorithmKeyedHash or AlgorithmSymCipher, then the returned *Public object will have a Unique field that is the digest
// of the sensitive data and the value of the object's seed in the sensitive area, computed using the object's name algorithm. If
// the Type field of inPublic is AlgorithmECC or AlgorithmRSA, then the returned *Public object will have a Unique field containing
// details about the public part of the key, computed from the private part of the key.
//
// The returned *CreationData will contain a digest computed from the values of PCRs selected by the creationPCR parameter at creation
// time in the PCRDigest field. It will also contain the provided outsideInfo in the OutsideInfo field. The returned *TkCreation ticket
// can be used to prove the association between the created object and the returned *CreationData via the TPMContext.CertifyCreation
// method.
func (t *TPMContext) CreatePrimary(primaryObject Handle, inSensitive *SensitiveCreate, inPublic *Public, outsideInfo Data, creationPCR PCRSelectionList, primaryObjectAuth interface{}, sessions ...*Session) (ResourceContext, *Public, *CreationData, Digest, *TkCreation, Name, error) {
	if inSensitive == nil {
		inSensitive = &SensitiveCreate{}
	}

	var objectHandle Handle

	var outPublic publicSized
	var creationData creationDataSized
	var creationHash Digest
	var creationTicket TkCreation
	var name Name

	if err := t.RunCommand(CommandCreatePrimary, sessions,
		HandleWithAuth{Handle: primaryObject, Auth: primaryObjectAuth}, Separator,
		sensitiveCreateSized{inSensitive}, publicSized{inPublic}, outsideInfo, creationPCR, Separator,
		&objectHandle, Separator,
		&outPublic, &creationData, &creationHash, &creationTicket, &name); err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}

	objectContext := &objectContext{handle: objectHandle, name: name}
	outPubCopy := outPublic.Ptr.Copy()
	if outPubCopy != nil {
		objectContext.public = *outPubCopy
	}
	t.addResourceContext(objectContext)

	return objectContext, outPublic.Ptr, creationData.Ptr, creationHash, &creationTicket, name, nil
}

// Clear executes the TPM2_Clear command to remove all context associated with the current owner. The command requires knowledge of
// the authorization value for either the platform or lockout hierarchy. The hierarchy is specified by passing either HandlePlatform
// or HandleLockout to authHandle. The command requires the user auth role for authHandle, provided via authHandleAuth.
//
// On successful completion, as well as the TPM having performed the operations associated with the TPM2_Clear command, this function
// will invalidate all ResourceContext instances of NV indices associated with the current owner, and all transient and persistent
// objects that reside in the storage and endorsement hierarchies.
//
// If the TPM2_Clear command has been disabled, a *TPMError error will be returned with an error code of ErrorDisabled.
func (t *TPMContext) Clear(authHandle Handle, authHandleAuth interface{}) error {
	var s []*sessionParam
	s, err := t.validateAndAppendSessionParam(s, HandleWithAuth{Handle: authHandle, Auth: authHandleAuth})
	if err != nil {
		return fmt.Errorf("error whilst processing handle with authorization for authHandle: %v", err)
	}

	ctx, err := t.runCommandWithoutProcessingResponse(CommandClear, s, authHandle)
	if err != nil {
		return err
	}

	authSession := ctx.sessionParams[0].session
	if authSession != nil {
		// If the HMAC key for this command includes the auth value for authHandle, the TPM will respond
		// with a HMAC generated with a key based on an empty auth value.
		ctx.sessionParams[0].session = authSession.copyWithNewAuthIfRequired(nil)
	}

	if err := t.processResponse(ctx); err != nil {
		return err
	}

	getHandles := func(handleType HandleType, out map[Handle]struct{}) error {
		handles, err := t.GetCapabilityHandles(handleType.BaseHandle(), CapabilityMaxProperties)
		if err != nil {
			return fmt.Errorf("cannot fetch handles from TPM after clear: %v", err)
		}
		var empty struct{}
		for _, handle := range handles {
			out[handle] = empty
		}
		return nil
	}

	handles := make(map[Handle]struct{})
	if err := getHandles(HandleTypeTransient, handles); err != nil {
		return err
	}
	if err := getHandles(HandleTypePersistent, handles); err != nil {
		return err
	}

	for _, rc := range t.resources {
		switch c := rc.(type) {
		case *objectContext:
			if _, exists := handles[c.handle]; exists {
				continue
			}
		case *nvIndexContext:
			if c.public.Attrs&AttrNVPlatformCreate > 0 {
				continue
			}
		case *sessionContext:
			continue
		}

		t.evictResourceContext(rc)
	}

	return nil
}

// ClearControl executes the TPM2_ClearControl command to enable or disable execution of the TPM2_Clear command (via the
// TPMContext.Clear function).
//
// If disable is true, then this command will disable the execution of TPM2_Clear. In this case, the command requires knowledge of
// the authorization value for the platform or lockout hierarchy. The hierarchy is specified via the authHandle parameter by
// setting it to either HandlePlatform or HandleLockout.
//
// If disable is false, then this command will enable execution of TPM2_Clear. In this case, the command requires knowledge of the
// authorization value for the platform hierarchy, and authHandle must be set to HandlePlatform. If authHandle is set to HandleOwner,
// a *TPMError error with an error code of ErrorAuthFail will be returned.
//
// The command requires the authorization with the user auth role for authHandle, provided via authHandleAuth.
func (t *TPMContext) ClearControl(authHandle Handle, disable bool, authHandleAuth interface{}) error {
	return t.RunCommand(CommandClearControl, nil,
		HandleWithAuth{Handle: authHandle, Auth: authHandleAuth}, Separator,
		disable)
}

// HierarchyChangeAuth executes the TPM2_HierarchyChangeAuth command to change the authorization value for the hierarchy associated
// with the authHandle parameter. The command requires authorization with the user auth role for authHandle, provided via
// authHandleAuth.
//
// If the value of newAuth is longer than the context integrity digest algorithm for the TPM, a *TPMParameterError error with an
// error code of ErrorSize will be returned.
//
// On successful completion, the authorization value for the hierarchy associated with authHandle will be set to the value of newAuth.
func (t *TPMContext) HierarchyChangeAuth(authHandle Handle, newAuth Auth, authHandleAuth interface{}, sessions ...*Session) error {
	var s []*sessionParam
	s, err := t.validateAndAppendSessionParam(s, HandleWithAuth{Handle: authHandle, Auth: authHandleAuth})
	if err != nil {
		return fmt.Errorf("error whilst processing handle with authorization for authHandle: %v", err)
	}
	s, err = t.validateAndAppendSessionParam(s, sessions)
	if err != nil {
		return fmt.Errorf("error whilst processing non-auth sessions: %v", err)
	}

	ctx, err := t.runCommandWithoutProcessingResponse(CommandHierarchyChangeAuth, s,
		authHandle, Separator,
		newAuth)
	if err != nil {
		return err
	}

	authSession := ctx.sessionParams[0].session
	if authSession != nil {
		// If the HMAC key for this command includes the auth value for authHandle, the TPM will respond
		// with a HMAC generated with a key that includes newAuth instead.
		ctx.sessionParams[0].session = authSession.copyWithNewAuthIfRequired(newAuth)
	}

	return t.processResponse(ctx)
}
