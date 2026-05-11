package main

import (
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/gogoproto/proto"
)

// EthAccount is a minimal copy of ethermint's EthAccount just for decoding
type EthAccount struct {
	BaseAccount *authtypes.BaseAccount `protobuf:"bytes,1,opt,name=base_account,json=baseAccount,proto3"`
	CodeHash    string                 `protobuf:"bytes,2,opt,name=code_hash,json=codeHash,proto3"`
}

func (m *EthAccount) Reset()         { *m = EthAccount{} }
func (m *EthAccount) String() string { return proto.CompactTextString(m) }
func (*EthAccount) ProtoMessage()    {}
