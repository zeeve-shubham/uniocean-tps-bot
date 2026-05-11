package main

import (
	"bytes"
	"crypto/subtle"
	"fmt"

	tmcrypto "github.com/cometbft/cometbft/crypto"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	proto "github.com/cosmos/gogoproto/proto"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
)

const ethSecp256k1Type = "eth_secp256k1"

func init() {
	proto.RegisterType((*EthPubKey)(nil), "ethermint.crypto.v1.ethsecp256k1.PubKey")
	proto.RegisterType((*EthPrivKey)(nil), "ethermint.crypto.v1.ethsecp256k1.PrivKey")
}

type EthPubKey struct {
	Key []byte `protobuf:"bytes,1,opt,name=key,proto3" json:"key,omitempty"`
}

func (m *EthPubKey) Address() cryptotypes.Address {
	if len(m.Key) == 0 {
		return nil
	}
	pubk, err := gethcrypto.DecompressPubkey(m.Key)
	if err != nil {
		return nil
	}
	return tmcrypto.Address(gethcrypto.PubkeyToAddress(*pubk).Bytes())
}

func (m *EthPubKey) Bytes() []byte {
	bz := make([]byte, len(m.Key))
	copy(bz, m.Key)
	return bz
}

func (m *EthPubKey) VerifySignature(msg, sig []byte) bool {
	if len(sig) == gethcrypto.SignatureLength {
		sig = sig[:len(sig)-1]
	}
	return gethcrypto.VerifySignature(m.Key, gethcrypto.Keccak256Hash(msg).Bytes(), sig)
}

func (m *EthPubKey) Equals(other cryptotypes.PubKey) bool {
	return m.Type() == other.Type() && bytes.Equal(m.Bytes(), other.Bytes())
}

func (m *EthPubKey) Type() string            { return ethSecp256k1Type }
func (m *EthPubKey) ProtoMessage()           {}
func (m *EthPubKey) Reset()                  { *m = EthPubKey{} }
func (m *EthPubKey) String() string          { return fmt.Sprintf("EthPubKeySecp256k1{%X}", m.Key) }
func (m *EthPubKey) XXX_MessageName() string { return "ethermint.crypto.v1.ethsecp256k1.PubKey" }

type EthPrivKey struct {
	Key []byte `protobuf:"bytes,1,opt,name=key,proto3" json:"key,omitempty"`
}

func (m *EthPrivKey) Bytes() []byte {
	bz := make([]byte, len(m.Key))
	copy(bz, m.Key)
	return bz
}

func (m *EthPrivKey) Sign(msg []byte) ([]byte, error) {
	digest := msg
	if len(digest) != gethcrypto.DigestLength {
		digest = gethcrypto.Keccak256Hash(msg).Bytes()
	}
	key, err := gethcrypto.ToECDSA(m.Key)
	if err != nil {
		return nil, err
	}
	return gethcrypto.Sign(digest, key)
}

func (m *EthPrivKey) PubKey() cryptotypes.PubKey {
	key, err := gethcrypto.ToECDSA(m.Key)
	if err != nil {
		return nil
	}
	return &EthPubKey{Key: gethcrypto.CompressPubkey(&key.PublicKey)}
}

func (m *EthPrivKey) Equals(other cryptotypes.LedgerPrivKey) bool {
	return m.Type() == other.Type() && subtle.ConstantTimeCompare(m.Bytes(), other.Bytes()) == 1
}

func (m *EthPrivKey) Type() string            { return ethSecp256k1Type }
func (m *EthPrivKey) ProtoMessage()           {}
func (m *EthPrivKey) Reset()                  { *m = EthPrivKey{} }
func (m *EthPrivKey) String() string          { return "EthPrivKey" }
func (m *EthPrivKey) XXX_MessageName() string { return "ethermint.crypto.v1.ethsecp256k1.PrivKey" }
