// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/NethermindEth/near-sffl/aggregator (interfaces: RpcAggregatorer)
//
// Generated by this command:
//
//	mockgen -destination=./mocks/rpc_aggregator.go -package=mocks github.com/NethermindEth/near-sffl/aggregator RpcAggregatorer
//

// Package mocks is a generated GoMock package.
package mocks

import (
	reflect "reflect"

	messages "github.com/NethermindEth/near-sffl/core/types/messages"
	gomock "go.uber.org/mock/gomock"
)

// MockRpcAggregatorer is a mock of RpcAggregatorer interface.
type MockRpcAggregatorer struct {
	ctrl     *gomock.Controller
	recorder *MockRpcAggregatorerMockRecorder
}

// MockRpcAggregatorerMockRecorder is the mock recorder for MockRpcAggregatorer.
type MockRpcAggregatorerMockRecorder struct {
	mock *MockRpcAggregatorer
}

// NewMockRpcAggregatorer creates a new mock instance.
func NewMockRpcAggregatorer(ctrl *gomock.Controller) *MockRpcAggregatorer {
	mock := &MockRpcAggregatorer{ctrl: ctrl}
	mock.recorder = &MockRpcAggregatorerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockRpcAggregatorer) EXPECT() *MockRpcAggregatorerMockRecorder {
	return m.recorder
}

// GetAggregatedCheckpointMessages mocks base method.
func (m *MockRpcAggregatorer) GetAggregatedCheckpointMessages(arg0, arg1 uint64) (*messages.CheckpointMessages, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetAggregatedCheckpointMessages", arg0, arg1)
	ret0, _ := ret[0].(*messages.CheckpointMessages)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetAggregatedCheckpointMessages indicates an expected call of GetAggregatedCheckpointMessages.
func (mr *MockRpcAggregatorerMockRecorder) GetAggregatedCheckpointMessages(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetAggregatedCheckpointMessages", reflect.TypeOf((*MockRpcAggregatorer)(nil).GetAggregatedCheckpointMessages), arg0, arg1)
}

// GetRegistryCoordinatorAddress mocks base method.
func (m *MockRpcAggregatorer) GetRegistryCoordinatorAddress(arg0 *string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetRegistryCoordinatorAddress", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// GetRegistryCoordinatorAddress indicates an expected call of GetRegistryCoordinatorAddress.
func (mr *MockRpcAggregatorerMockRecorder) GetRegistryCoordinatorAddress(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetRegistryCoordinatorAddress", reflect.TypeOf((*MockRpcAggregatorer)(nil).GetRegistryCoordinatorAddress), arg0)
}

// ProcessSignedCheckpointTaskResponse mocks base method.
func (m *MockRpcAggregatorer) ProcessSignedCheckpointTaskResponse(arg0 *messages.SignedCheckpointTaskResponse) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ProcessSignedCheckpointTaskResponse", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// ProcessSignedCheckpointTaskResponse indicates an expected call of ProcessSignedCheckpointTaskResponse.
func (mr *MockRpcAggregatorerMockRecorder) ProcessSignedCheckpointTaskResponse(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ProcessSignedCheckpointTaskResponse", reflect.TypeOf((*MockRpcAggregatorer)(nil).ProcessSignedCheckpointTaskResponse), arg0)
}

// ProcessSignedOperatorSetUpdateMessage mocks base method.
func (m *MockRpcAggregatorer) ProcessSignedOperatorSetUpdateMessage(arg0 *messages.SignedOperatorSetUpdateMessage) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ProcessSignedOperatorSetUpdateMessage", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// ProcessSignedOperatorSetUpdateMessage indicates an expected call of ProcessSignedOperatorSetUpdateMessage.
func (mr *MockRpcAggregatorerMockRecorder) ProcessSignedOperatorSetUpdateMessage(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ProcessSignedOperatorSetUpdateMessage", reflect.TypeOf((*MockRpcAggregatorer)(nil).ProcessSignedOperatorSetUpdateMessage), arg0)
}

// ProcessSignedStateRootUpdateMessage mocks base method.
func (m *MockRpcAggregatorer) ProcessSignedStateRootUpdateMessage(arg0 *messages.SignedStateRootUpdateMessage) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ProcessSignedStateRootUpdateMessage", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// ProcessSignedStateRootUpdateMessage indicates an expected call of ProcessSignedStateRootUpdateMessage.
func (mr *MockRpcAggregatorerMockRecorder) ProcessSignedStateRootUpdateMessage(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ProcessSignedStateRootUpdateMessage", reflect.TypeOf((*MockRpcAggregatorer)(nil).ProcessSignedStateRootUpdateMessage), arg0)
}
