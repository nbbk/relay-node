# 选项 D：静态托管（只解决远程配置）

只管 `/client-config.json` 这一条路：把签好的文件放固定地方，原样 served。

**注意：订阅（`/sub/*`）与公告（`/api/v1/public-info`）仍要走选项 A/B/C/E 的反代，静态托管管不了。** 这个选项是“远程配置单独放出来”，不是完整网关。

## 方法一：GitHub Pages

1. 建一个仓库（公开即可——配置文件是签过名的公开文件，签名防篡改，不怕公开；不要把私钥放进去）。
2. 把签好的 `client-config.json` 放仓库根目录。
3. 仓库 Settings -> Pages -> Source 选 main 分支，保存。
4. 地址就是 `https://<用户名>.github.io/<仓库名>/client-config.json`，把这个地址填到客户端远程配置的 GitHub Raw 那一路，或填进 Worker 的 `CONFIG_UPSTREAM`。

更新：重新签名后把文件推上去就行。注意 Pages 有几分钟缓存，客户端拉取有本地缓存兜底，不用急。

## 方法二：自建 nginx 静态目录

1. 把签好的 `client-config.json` 放 `/var/www/sub-gateway/`。
2. nginx 配置里 include 本目录的 `nginx-snippet.conf`（路径按实改）。
3. `nginx -t && systemctl reload nginx`。
4. 地址就是 `https://<你的域名>/client-config.json`。

```bash
curl -s https://example.com/client-config.json | sha256sum
# 跟签好的那份 sha256 对一下，对上就是字节精确
```

## 轮换

重新签名后覆盖文件即可（GitHub Pages 推上去；nginx 直接覆盖）。客户端按版本号判断新旧，旧配置在本地缓存里兜底，不会断。
