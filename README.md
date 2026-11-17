# Yu

> 疏而不堵，因果自驱。

[English](README.en.md) | 简体中文

Yu 是一个类型驱动的 Go HTTP 框架。

你只写不带锁的纯函数——原子技能（atomic skill）。把函数交给路由层，引擎在启动期根据参数与返回值类型自动编排拓扑：**无依赖的技能自动多核并行，同 Key 的写操作自动分片排队**。没有中间件，没有 service / controller 的界线，也没有手写的并发胶水。

- 路由处理逻辑 = 一组普通函数，因果由 **Go 类型**决定
- 同 wave 的无依赖技能并发执行，跨 wave 按产出值顺序推进
- 每种数据类型只有一个生产者，因果不会含糊
- `Response` 是终止信号而非数据：同 wave 最多一个生产者，允许跨 wave 早退
- 单个技能 panic 只影响当前请求，不会带崩进程或分片队列
- 启动期编译：歧义因果、不可达依赖、非法签名直接失败

## 名字的由来

Yu 取自「大禹治水」。大禹治水不靠筑坝硬堵，而是疏浚河道，让水自己找到去处。这个框架遵循同一原则：不靠中间件、锁和胶水代码去堵住并发，而是把函数之间的因果交给 Go 类型，由引擎在启动期梳理成拓扑，让请求自己流过去。

> 疏而不堵，因果自驱。——Yu（大禹）

要求 Go 1.26+。

## 安装

```bash
go get github.com/Okysu/Yu
```

## 文档与示例

- [快速开始](docs/quickstart.md)（English）
- [进阶用法](docs/advanced.md)（English）
- [API 参考](docs/api.md)（English）
- [基准测试](docs/benchmarks.md)（English）
- 可运行示例：[`examples/quickstart`](examples/quickstart)、[`examples/advanced`](examples/advanced)、[`examples/middleware`](examples/middleware)、[`examples/sse`](examples/sse)

## 快速开始

```go
package main

import (
	"database/sql"
	"time"

	yu "github.com/Okysu/Yu"
)

type User struct {
	ID   int
	Name string
}

type Stats struct {
	Orders int
}

func LoadUser(db *sql.DB, req yu.Request) (User, error) {
	// SELECT ... WHERE id = req.Param("id")
	return User{}, nil
}

func LoadStats(db *sql.DB, req yu.Request) (Stats, error) {
	// SELECT ...
	return Stats{}, nil
}

func Render(u User, s Stats) yu.Response {
	return yu.JSON(200, map[string]any{"user": u, "stats": s})
}

func main() {
	db, _ := sql.Open("postgres", "dsn")

	e := yu.New()
	e.Provide(db)

	e.GET("/users/:id",
		yu.WithTimeout(2*time.Second),
		LoadUser,  // wave 0
		LoadStats, // wave 0，与 LoadUser 并发
		Render,    // wave 1，依赖 User + Stats
	)

	e.Run(":8080")
}
```

`LoadUser` 与 `LoadStats` 都只依赖 `*sql.DB` 和 `yu.Request`，因此进入同一个 wave 并发执行；`Render` 依赖两者产出的类型，自动进入下一个 wave。你不需要声明依赖、启动 goroutine 或加锁。

## 核心概念

### 原子技能

一个技能就是一个普通函数：

```go
func LoadUser(db *sql.DB, req yu.Request) (User, error)
```

- 入参从类型黑板（blackboard）解析：`yu.Request`、`context.Context`、全局单例、上游技能产出
- 返回值写入黑板供下游使用；`error` 必须是最后一个返回值，非 nil 时中止整条流水线
- 返回非零 `yu.Response` 即终止路由并作为响应

### 执行模型

1. **编译期**：反射读取每个技能的输入/输出类型，做拓扑排序，切成若干 wave
2. **运行期**：逐 wave 执行，wave 内并发、wave 间顺序；每个 wave 读取的始终是上一波结束时的黑板
3. **单赋值**：同一数据类型只允许一个技能生产。需要「加工后的值」就定义新类型——类型的身份就是因果的身份
4. **终止信号**：`yu.Response` 只能被生产，不能作为输入；同一 wave 最多一个生产者；不同 wave 可以各有一个，用于「缓存命中即返回，未命中再回源」这类早退

### 分片调度

同 Key 的写操作需要串行时，显式声明：

```go
func ParseOrder(req yu.Request) (Order, error) { /* ... */ }
func SaveOrder(db *sql.DB, o Order) yu.Response { /* ... */ }

e.POST("/orders/:id",
	yu.PartitionBy(func(req yu.Request) string {
		return req.Param("id")
	}),
	ParseOrder,
	SaveOrder,
)
```

相同 key 的请求固定到同一条 lane 串行执行，不同 key 并行。排队与执行期间都响应 context 取消，超时不会被积压的队列无限拉长。

### 全局单例

```go
e.Provide(db, logger) // 按具体类型注入
```

在 `Run` 之前注册；重复类型或 typed-nil 会在启动期 panic。

## Request 访问器

| 方法 | 说明 |
|---|---|
| `req.Param("id")` | 路径参数（`:id` / `*filepath`） |
| `req.QueryDefault("q", "def")` | 查询参数首值，缺失时取默认值 |
| `req.Get("Content-Type")` | 请求头首值（大小写不敏感） |
| `req.Body()` | 原始请求体（只读契约） |
| `req.BindJSON(&dst)` | 按 JSON 解析请求体 |
| `req.Context` | 带超时/取消信号的 context |

容器字段是私有的，只提供只读访问器，避免同 wave 内的技能互相写坏共享 map。

## 路由

| 选项 | 说明 |
|---|---|
| `yu.WithTimeout(d)` | 路由级超时，超时返回 504 |
| `yu.WithBodyLimit(n)` | 请求体上限，超限返回 413（默认不限制） |
| `yu.WithInlineWaveLimit(n)` | 含 ≤n 个技能的 wave 顺序执行、不启动 goroutine（默认 1，仅单节点） |
| `yu.PartitionBy(keyFn, workers...)` | 分片串行调度，默认 32 条 lane |

匹配优先级：静态段 > `:param` > `*catch-all`。尾部斜杠会被忽略；同一路径位置不允许两个不同的通配符。路径存在但方法不匹配时返回 405（带 `Allow`），否则 404。

需要接入标准 `net/http` 处理器时，用 `r.Raw(pattern, func(w, req))` 或 `r.Handle(pattern, handler)`；它们对全部方法生效并绕过 DAG，路径参数通过 `req.PathValue(name)` 读取。

`Group("/api", nodes...)` 与 `Use(nodes...)` 中的节点不是中间件，它们和路由节点一起并入同一条类型驱动 DAG，顺序完全由类型决定。

## 编译期规则

以下情况会在启动期直接 panic，而不是在运行期给出错误答案：

- 路由没有任何技能产出 `yu.Response`
- 同一数据类型被多个技能生产
- 同一 wave 内多个技能产出 `yu.Response`
- `yu.Response` 出现在输入参数中
- `error` 不是最后一个返回值
- 使用变参函数
- 依赖不可解析或存在环
- 同一路径位置出现两个不同的通配符
- 路由 pattern 为空
- `Provide` 重复注册同一类型或传入 typed-nil

## 可观测性

```go
e.EnableDebugDAG("/debug/dag")
```

打开 `/debug/dag` 查看每条路由编译后的 wave 拓扑；`/debug/dag?format=json` 返回同样的结构化数据。调试路由自身不会出现在列表中。

## 项目结构

```
yu.go         Engine、Request、Response、HTTP 入口
dag.go        技能反射、拓扑编译、wave 调度、panic/超时处理
http_error.go 带状态码的错误映射
router.go     trie 路由与路由条目
trie.go       路由树节点
group.go      路由分组
partition.go  分片调度器与路由选项
debug.go      拓扑可视化
docs/         文档：quickstart、advanced、API、benchmarks
examples/     可运行示例：quickstart、advanced、middleware、sse
benchmarks/   独立模块的框架对比基准
```

## 设计约束

- 技能必须是纯函数：不要修改 `Request`、单例或上游产出的共享对象。框架保证容器只读，但对象内部状态仍由你负责。
- 并发 wave 快速失败：第一个返回 error 或非零 `Response` 的节点会取消同 wave 的其他节点并立即返回；但取消是协作式的，不会抢占正在执行的函数，也不会回滚已开始的副作用。单节点/inline wave 顺序执行。
- 横切能力（CORS、压缩、访问日志、指标）放在引擎外：`*Engine` 实现了 `http.Handler`，用标准 net/http 中间件包装即可。
- 技能 panic 会被恢复并记录到服务端日志；客户端只会收到 500，不暴露内部细节。
- 需要在「同一个值上做不同阶段的加工」时，请定义不同类型：

```go
type Price struct{ Cents int }
type DiscountedPrice Price

func FetchPrice(req yu.Request) Price
func ApplyDiscount(p Price) DiscountedPrice
func Checkout(p DiscountedPrice) yu.Response
```

## License

[MIT](LICENSE)
