// internal/api/derivative_handlers.go

package api

import (
	"net/http"
	"strconv"

	"github.com/eyes2near/b-trading/internal/models"
	"github.com/gin-gonic/gin"
)

// ListDerivativeRules 列出所有规则
func (h *Handler) ListDerivativeRules(c *gin.Context) {
	rules, err := h.ruleRepo.ListAll(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"rules": rules})
}

// CreateDerivativeRule 创建规则
func (h *Handler) CreateDerivativeRule(c *gin.Context) {
	var rule models.DerivativeRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 验证规则
	if err := h.derivativeEngine.ValidateRule(&rule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.ruleRepo.Create(c.Request.Context(), &rule); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 刷新缓存
	h.derivativeEngine.RefreshRules(c.Request.Context())

	c.JSON(http.StatusCreated, gin.H{"rule": rule})
}

// GetDerivativeRule 获取规则详情
func (h *Handler) GetDerivativeRule(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	rule, err := h.ruleRepo.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "rule not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"rule": rule})
}

// UpdateDerivativeRule 更新规则
func (h *Handler) UpdateDerivativeRule(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var rule models.DerivativeRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	rule.ID = uint(id)

	// 验证规则
	if err := h.derivativeEngine.ValidateRule(&rule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.ruleRepo.Update(c.Request.Context(), &rule); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 刷新缓存
	h.derivativeEngine.RefreshRules(c.Request.Context())

	c.JSON(http.StatusOK, gin.H{"rule": rule})
}

// DeleteDerivativeRule 删除规则
func (h *Handler) DeleteDerivativeRule(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := h.ruleRepo.Delete(c.Request.Context(), uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 刷新缓存
	h.derivativeEngine.RefreshRules(c.Request.Context())

	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// RefreshRuleCache 手动刷新缓存
func (h *Handler) RefreshRuleCache(c *gin.Context) {
	if err := h.derivativeEngine.RefreshRules(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "cache refreshed"})
}

// CheckRuleConflicts 检查规则冲突
func (h *Handler) CheckRuleConflicts(c *gin.Context) {
	conflicts, err := h.derivativeEngine.CheckConflicts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"conflicts": conflicts,
		"count":     len(conflicts),
	})
}
