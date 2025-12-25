// internal/service/order_lock.go

package service

import (
	"sync"
)

// OrderLockManager 订单级别的内存锁管理器
type OrderLockManager struct {
	locks sync.Map // map[uint]*sync.Mutex
}

// NewOrderLockManager 创建锁管理器
func NewOrderLockManager() *OrderLockManager {
	return &OrderLockManager{}
}

// Lock 获取指定订单的锁
func (m *OrderLockManager) Lock(orderID uint) {
	lock := m.getLock(orderID)
	lock.Lock()
}

// Unlock 释放指定订单的锁
func (m *OrderLockManager) Unlock(orderID uint) {
	lock := m.getLock(orderID)
	lock.Unlock()
}

// getLock 获取或创建订单锁
func (m *OrderLockManager) getLock(orderID uint) *sync.Mutex {
	actual, _ := m.locks.LoadOrStore(orderID, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// WithLock 使用锁执行函数
func (m *OrderLockManager) WithLock(orderID uint, fn func() error) error {
	m.Lock(orderID)
	defer m.Unlock(orderID)
	return fn()
}
