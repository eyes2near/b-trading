// internal/derivative/evaluator.go

package derivative

import (
	"fmt"

	"github.com/Knetic/govaluate"
)

// Evaluator 表达式求值器
type Evaluator struct{}

// NewEvaluator 创建求值器
func NewEvaluator() *Evaluator {
	return &Evaluator{}
}

// Evaluate 求值表达式
func (e *Evaluator) Evaluate(expression string, variables map[string]interface{}) (float64, error) {
	expr, err := govaluate.NewEvaluableExpression(expression)
	if err != nil {
		return 0, fmt.Errorf("invalid expression syntax: %w", err)
	}

	result, err := expr.Evaluate(variables)
	if err != nil {
		return 0, fmt.Errorf("expression evaluation failed: %w", err)
	}

	// 转换为 float64
	switch v := result.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	default:
		return 0, fmt.Errorf("expression result is not a number: %T", result)
	}
}

// Validate 验证表达式语法
func (e *Evaluator) Validate(expression string) error {
	_, err := govaluate.NewEvaluableExpression(expression)
	if err != nil {
		return fmt.Errorf("invalid expression: %w", err)
	}
	return nil
}

// ValidateWithVariables 验证表达式并检查变量
func (e *Evaluator) ValidateWithVariables(expression string, requiredVars []string) error {
	expr, err := govaluate.NewEvaluableExpression(expression)
	if err != nil {
		return fmt.Errorf("invalid expression: %w", err)
	}

	// 获取表达式中使用的变量
	usedVars := expr.Vars()

	// 检查是否使用了未知变量
	knownVars := map[string]bool{
		"d_price":         true,
		"p_price":         true,
		"fill_price":      true,
		"delta":           true,
		"delta_value":     true,
		"delta_contracts": true,
		"cum_qty":         true,
		"contract_size":   true,
	}

	for _, v := range usedVars {
		if !knownVars[v] {
			return fmt.Errorf("unknown variable in expression: %s", v)
		}
	}

	return nil
}
