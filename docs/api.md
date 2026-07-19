# HTTP API v1

管理端默认监听 `0.0.0.0:8080`，无鉴权；Windows恢复端监听随机的 `127.0.0.1` 端口。所有JSON错误使用：

```json
{ "error": "中文错误说明" }
```

## 通用

| 方法 | 路径            | 说明                                 |
| ---- | --------------- | ------------------------------------ |
| GET  | `/api/health`   | 进程、模式和版本健康检查             |
| GET  | `/api/runtime`  | `server` 或 `offline` 运行模式       |
| GET  | `/api/fs?path=` | 浏览允许的目录；管理端限制在映射根内 |

## NAS管理端

| 方法           | 路径                                        | 说明                                         |
| -------------- | ------------------------------------------- | -------------------------------------------- |
| GET/POST       | `/api/tasks`                                | 列出或创建任务                               |
| GET/PUT/DELETE | `/api/tasks/{id}`                           | 查看、更新或二次确认删除任务                 |
| POST           | `/api/tasks/{id}/run`                       | 手动加入串行队列                             |
| POST           | `/api/tasks/{id}/pause`                     | 当前对象完成后暂停                           |
| POST           | `/api/tasks/{id}/resume`                    | 继续暂停任务                                 |
| POST           | `/api/tasks/{id}/reconnect`                 | 只读验证或确认新的WebDAV连接                 |
| GET            | `/api/tasks/{id}/snapshots`                 | 版本和归档索引                               |
| GET            | `/api/tasks/{id}/files?snapshot=`           | 指定版本文件列表                             |
| GET            | `/api/tasks/{id}/runs`                      | 最近100次运行记录                            |
| POST           | `/api/tasks/{id}/check`                     | 名称、大小和未引用对象检查                   |
| POST           | `/api/tasks/{id}/cleanup`                   | 输入任务密码后清理未引用对象                 |
| POST           | `/api/tasks/{id}/restore`                   | 从WebDAV恢复到NAS映射目录                    |
| POST           | `/api/tasks/{id}/restore-imported`          | 从已下载且保留 `objects/` 结构的任务目录恢复 |
| POST           | `/api/tasks/{id}/plan`                      | 导出 `.backup-plan` JSON                     |
| POST           | `/api/tasks/{id}/archive-delete`            | 归档模式删除文件记录                         |
| POST           | `/api/tasks/{id}/snapshots/{snapshot}/lock` | 锁定或解锁版本                               |
| DELETE         | `/api/tasks/{id}/snapshots/{snapshot}`      | 删除未锁定的整个版本                         |
| POST           | `/api/remotes/discover`                     | 扫描远端一级任务目录                         |
| POST           | `/api/remotes/attach`                       | 从远端索引重建本地任务                       |
| GET/PUT        | `/api/settings`                             | 全局并发、限速和时区                         |

任务、WebDAV和删除密码从不出现在响应中。

## Windows离线端

| 方法 | 路径                   | 说明                           |
| ---- | ---------------------- | ------------------------------ |
| POST | `/api/offline/open`    | 打开完整任务目录并输入任务密码 |
| POST | `/api/offline/select`  | 选择版本                       |
| GET  | `/api/offline/files`   | 当前版本文件列表               |
| POST | `/api/offline/restore` | 恢复所选文件到本地目录         |
