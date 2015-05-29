package libkbfs

import (
	"errors"
	"testing"

	"code.google.com/p/gomock/gomock"
	"github.com/keybase/client/go/libkb"
	keybase1 "github.com/keybase/client/protocol/go"
)

func keyManagerInit(t *testing.T) (mockCtrl *gomock.Controller,
	config *ConfigMock) {
	ctr := NewSafeTestReporter(t)
	mockCtrl = gomock.NewController(ctr)
	config = NewConfigMock(mockCtrl, ctr)
	keyman := &KeyManagerStandard{config}
	config.SetKeyManager(keyman)
	return
}

func keyManagerShutdown(mockCtrl *gomock.Controller, config *ConfigMock) {
	config.ctr.CheckForFailures()
	mockCtrl.Finish()
}

func expectCachedGetTLFCryptKey(config *ConfigMock, rmd *RootMetadata) {
	config.mockKcache.EXPECT().GetTLFCryptKey(rmd.ID, KeyVer(0)).Return(TLFCryptKey{}, nil)
}

func expectUncachedGetTLFCryptKey(config *ConfigMock, rmd *RootMetadata, uid keybase1.UID, subkey CryptPublicKey) {
	config.mockKcache.EXPECT().GetTLFCryptKey(rmd.ID, KeyVer(0)).
		Return(TLFCryptKey{}, errors.New("NONE"))

	// get the xor'd key out of the metadata
	config.mockKbpki.EXPECT().GetCurrentCryptPublicKey().Return(subkey, nil)
	config.mockCrypto.EXPECT().DecryptTLFCryptKeyClientHalf(TLFEphemeralPublicKey{}, gomock.Any()).Return(TLFCryptKeyClientHalf{}, nil)

	// get the server-side half and retrieve the real secret key
	config.mockKops.EXPECT().GetTLFCryptKeyServerHalf(
		rmd.ID, rmd.LatestKeyVersion(), subkey).Return(TLFCryptKeyServerHalf{}, nil)
	config.mockCrypto.EXPECT().UnmaskTLFCryptKey(TLFCryptKeyServerHalf{}, TLFCryptKeyClientHalf{}).Return(TLFCryptKey{}, nil)

	// now put the key into the cache
	config.mockKcache.EXPECT().PutTLFCryptKey(rmd.ID, rmd.LatestKeyVersion(), TLFCryptKey{}).
		Return(nil)
}

func expectUncachedGetBlockCryptKey(
	config *ConfigMock, id BlockID, rmd *RootMetadata) {
	config.mockKcache.EXPECT().GetBlockCryptKey(id).
		Return(BlockCryptKey{}, errors.New("NONE"))

	expectCachedGetTLFCryptKey(config, rmd)

	config.mockKops.EXPECT().GetBlockCryptKeyServerHalf(id).Return(BlockCryptKeyServerHalf{}, nil)
	config.mockCrypto.EXPECT().UnmaskBlockCryptKey(BlockCryptKeyServerHalf{}, TLFCryptKey{}).Return(BlockCryptKey{}, nil)

	// now put the key into the cache
	config.mockKcache.EXPECT().PutBlockCryptKey(id, BlockCryptKey{}).Return(nil)
}

func expectRekey(config *ConfigMock, rmd *RootMetadata, userID keybase1.UID) {
	// generate new keys
	config.mockCrypto.EXPECT().MakeRandomTLFKeys().Return(TLFPublicKey{}, TLFPrivateKey{}, TLFEphemeralPublicKey{}, TLFEphemeralPrivateKey{}, TLFCryptKey{}, nil)
	config.mockCrypto.EXPECT().MakeRandomTLFCryptKeyServerHalf().Return(TLFCryptKeyServerHalf{}, nil)

	subkey := MakeFakeCryptPublicKeyOrBust("crypt public key")
	config.mockKbpki.EXPECT().GetCryptPublicKeys(gomock.Any()).
		Return([]CryptPublicKey{subkey}, nil)

	// make keys for the one device
	config.mockCrypto.EXPECT().MaskTLFCryptKey(TLFCryptKeyServerHalf{}, TLFCryptKey{}).Return(TLFCryptKeyClientHalf{}, nil)
	xbuf := []byte{42}
	config.mockCrypto.EXPECT().EncryptTLFCryptKeyClientHalf(TLFEphemeralPrivateKey{}, subkey, TLFCryptKeyClientHalf{}).Return(xbuf, nil)
	config.mockKops.EXPECT().PutTLFCryptKeyServerHalf(
		rmd.ID, KeyVer(1), userID, subkey, TLFCryptKeyServerHalf{}).Return(nil)
	// now put the key into the cache
	config.mockKcache.EXPECT().PutTLFCryptKey(rmd.ID, KeyVer(1), TLFCryptKey{}).Return(nil)
}

func pathFromRMD(config *ConfigMock, rmd *RootMetadata) Path {
	return Path{rmd.ID, []PathNode{PathNode{
		BlockPointer{BlockID{}, rmd.data.Dir.KeyVer, 0, keybase1.MakeTestUID(0), 0},
		rmd.GetDirHandle().ToString(config),
	}}}
}

func TestKeyManagerCachedSecretKeySuccess(t *testing.T) {
	mockCtrl, config := keyManagerInit(t)
	defer keyManagerShutdown(mockCtrl, config)

	_, id, h := makeID(config)
	rmd := NewRootMetadata(h, id)
	rmd.AddNewKeys(DirKeyBundle{})

	expectCachedGetTLFCryptKey(config, rmd)

	if _, err := config.KeyManager().GetTLFCryptKey(
		pathFromRMD(config, rmd), rmd); err != nil {
		t.Errorf("Got error on GetTLFCryptKey: %v", err)
	}
}

func TestKeyManagerUncachedSecretKeySuccess(t *testing.T) {
	mockCtrl, config := keyManagerInit(t)
	defer keyManagerShutdown(mockCtrl, config)

	uid, id, h := makeID(config)
	rmd := NewRootMetadata(h, id)

	subkey := MakeFakeCryptPublicKeyOrBust("crypt public key")
	dirKeyBundle := DirKeyBundle{
		RKeys: map[keybase1.UID]map[libkb.KIDMapKey][]byte{
			uid: map[libkb.KIDMapKey][]byte{
				subkey.KID.ToMapKey(): []byte{},
			},
		},
	}
	rmd.AddNewKeys(dirKeyBundle)

	expectUncachedGetTLFCryptKey(config, rmd, uid, subkey)

	if _, err := config.KeyManager().GetTLFCryptKey(
		pathFromRMD(config, rmd), rmd); err != nil {
		t.Errorf("Got error on GetTLFCryptKey: %v", err)
	}
}

func TestKeyManagerUncachedSecretBlockKeySuccess(t *testing.T) {
	mockCtrl, config := keyManagerInit(t)
	defer keyManagerShutdown(mockCtrl, config)

	_, id, h := makeID(config)
	rmd := NewRootMetadata(h, id)
	rmd.AddNewKeys(DirKeyBundle{})
	rootID := BlockID{42}

	expectUncachedGetBlockCryptKey(config, rootID, rmd)

	if _, err := config.KeyManager().GetBlockCryptKey(
		pathFromRMD(config, rmd), rootID, rmd); err != nil {
		t.Errorf("Got error on getBlockCryptKey: %v", err)
	}
}

func TestKeyManagerGetUncachedBlockKeyFailNewKey(t *testing.T) {
	mockCtrl, config := keyManagerInit(t)
	defer keyManagerShutdown(mockCtrl, config)

	u, id, h := makeID(config)
	rmd := NewRootMetadata(h, id)

	rmd.data.Dir.Type = Dir
	// Set the key id in the future.
	rmd.data.Dir.KeyVer = 1

	rootID := BlockID{42}
	node := PathNode{BlockPointer{rootID, 1, 0, u, 0}, ""}
	p := Path{id, []PathNode{node}}

	// we'll check the cache, but then fail before getting the read key
	expectedErr := &NewKeyVersionError{rmd.GetDirHandle().ToString(config), 1}
	config.mockKcache.EXPECT().GetBlockCryptKey(rootID).Return(
		BlockCryptKey{}, errors.New("NOPE"))

	if _, err := config.KeyManager().GetBlockCryptKey(
		p, rootID, rmd); err == nil {
		t.Errorf("Got no expected error on GetBlockCryptKey")
	} else if err.Error() != expectedErr.Error() {
		t.Errorf("Got unexpected error on GetBlockCryptKey: %v", err)
	}
}

func TestKeyManagerRekeySuccess(t *testing.T) {
	mockCtrl, config := keyManagerInit(t)
	defer keyManagerShutdown(mockCtrl, config)

	u, id, h := makeID(config)
	rmd := NewRootMetadata(h, id)
	rmd.AddNewKeys(DirKeyBundle{})

	expectRekey(config, rmd, u)

	if err := config.KeyManager().Rekey(rmd); err != nil {
		t.Errorf("Got error on rekey: %v", err)
	} else if rmd.LatestKeyVersion() != 1 {
		t.Errorf("Bad key version after rekey: %d", rmd.LatestKeyVersion())
	}
}
