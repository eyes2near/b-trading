| 变量名 | 类型 | 说明 | 示例值 |
| :--- | :--- | :--- | :--- |
| d_price | float64 | 衍生品标的当前价格 | 30000.50 |
| p_price | float64 | 主订单标的当前价格 | 30005.00 |
| fill_price | float64 | 本次成交价格 | 30002.00 |
| delta | float64 | 本次成交增量（原始单位） | 0.05 |
| delta_value | float64 | 本次成交价值 (USDT) | 1500.10 |
| delta_contracts | float64 | 本次成交合约张数（coinm主订单） | 15 |
| cum_qty | float64 | 累计成交数量 | 0.15 |
| contract_size | float64 | 目标合约面值 | 100 |

## API 接口文档

### 页面路由
| 方法 | 路径 | 描述 | 参数 |
| :--- | :--- | :--- | :--- |
| GET | `/` | 渲染仪表盘页面 | 无 |

### 交易流程管理 (Flows)
| 方法 | 路径 | 描述 | 请求参数 (Form/Path) |
| :--- | :--- | :--- | :--- |
| POST | `/api/flows` | 创建交易流程并下单 | `market_type`, `quantity`, `price`, `direction`, `order_type`, `symbol_spot`/`resolved_symbol` 等 |
| GET | `/api/flows` | 获取活跃流程列表 (HTML) | 无 |
| GET | `/api/flows/:id` | 获取流程详情 (HTML) | `id` (Path) |
| POST | `/api/flows/:id/cancel` | 取消交易流程 | `id` (Path) |

### 市场数据 (Market Data)
| 方法 | 路径 | 描述 | 请求参数 |
| :--- | :--- | :--- | :--- |
| GET | `/api/prices/spot/:symbol` | 获取现货价格 | `symbol` (Path) |
| GET | `/api/prices/coinm/:symbol` | 获取币本位合约价格 | `symbol` (Path) |
| GET | `/api/coinm/quarter-symbols/:base` | 获取币本位季度合约代码 | `base` (Path, e.g., BTC) |

### 衍生品规则管理 (Derivative Rules)
| 方法 | 路径 | 描述 | 请求参数 (JSON/Path) |
| :--- | :--- | :--- | :--- |
| GET | `/api/derivative-rules` | 获取规则列表 | 无 |
| POST | `/api/derivative-rules` | 创建新规则 | JSON Body (Rule Object) |
| GET | `/api/derivative-rules/:id` | 获取规则详情 | `id` (Path) |
| PUT | `/api/derivative-rules/:id` | 更新规则 | `id` (Path), JSON Body |
| DELETE | `/api/derivative-rules/:id` | 删除规则 | `id` (Path) |
| POST | `/api/derivative-rules/refresh` | 刷新规则缓存 | 无 |
| GET | `/api/derivative-rules/conflicts` | 检查规则冲突 | 无 |

### Webhook
| 方法 | 路径 | 描述 | Headers / Body |
| :--- | :--- | :--- | :--- |
| POST | `/internal/webhook/binance` | 处理 Binance Webhook | Headers: `X-Webhook-Delivery-ID` 等<br>Body: JSON Event |
