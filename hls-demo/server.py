import base64
import json
import os
import socket
import struct
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).parent


def kodi_address():
    with socket.create_connection(('127.0.0.1', 9090), timeout=5) as sock:
        key = base64.b64encode(os.urandom(16)).decode()
        sock.sendall((f'GET /jsonrpc HTTP/1.1\r\nHost: localhost:9090\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n').encode())
        response = b''
        while not response.endswith(b'\r\n\r\n'):
            response += sock.recv(1)
        if b' 101 ' not in response:
            raise ValueError('Kodi WebSocket 连接失败')

        def read(n):
            data = b''
            while len(data) < n:
                part = sock.recv(n - len(data))
                if not part:
                    raise ValueError('Kodi 连接关闭')
                data += part
            return data

        def rpc(method, params, ident):
            payload = json.dumps(dict(jsonrpc='2.0', method=method, params=params, id=ident)).encode()
            mask = os.urandom(4)
            length = len(payload)
            header = bytes([129, 128 | length]) if length < 126 else bytes([129, 254]) + struct.pack('!H', length)
            sock.sendall(header + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(payload)))
            while True:
                head = read(2)
                length = head[1] & 127
                if length == 126:
                    length = struct.unpack('!H', read(2))[0]
                elif length == 127:
                    length = struct.unpack('!Q', read(8))[0]
                server_mask = read(4) if head[1] & 128 else None
                data = read(length)
                if server_mask:
                    data = bytes(b ^ server_mask[i % 4] for i, b in enumerate(data))
                if head[0] & 15 == 8:
                    raise ValueError('Kodi 连接关闭')
                if head[0] & 15 != 1:
                    continue
                result = json.loads(data)
                if result.get('id') == ident:
                    if 'error' in result:
                        raise ValueError(result['error']['message'])
                    return result['result']

        players = rpc('Player.GetActivePlayers', {}, 1)
        player = next((p for p in players if p['type'] == 'video'), None)
        if not player:
            raise ValueError('Kodi 未播放视频。请先播放 NHK World，或点击“获取 NHK 地址”。')
        item = rpc('Player.GetItem', {'playerid': player['playerid'], 'properties': ['file']}, 2)['item']
        url = item.get('file', '').split('|')[0]
        if not url.startswith('https://'):
            raise ValueError('Kodi 没有返回 HTTPS 直链（可能只返回 plugin://）。可改用 NHK 地址按钮。')
        return {'url': url, 'source': 'Kodi 当前播放项'}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        try:
            if self.path == '/api/kodi':
                self.reply(200, kodi_address())
            elif self.path == '/api/nhk':
                with urllib.request.urlopen('https://www3.nhk.or.jp/nhkworld/app/tv/hlslive_android_v2.json', timeout=15) as response:
                    data = json.load(response)
                self.reply(200, {'url': data['tv']['wstrm'], 'source': 'NHK 频道信息（同 Kodi 插件使用的接口）'})
            elif self.path in ('/', '/index.html', '/hls.min.js'):
                filename = 'hls.min.js' if self.path == '/hls.min.js' else 'index.html'
                content = (ROOT / filename).read_bytes()
                self.send_response(200)
                self.send_header('Content-Type', 'application/javascript' if filename.endswith('.js') else 'text/html; charset=utf-8')
                self.send_header('Content-Length', str(len(content)))
                self.end_headers()
                self.wfile.write(content)
            else:
                self.reply(404, {'error': 'Not found'})
        except Exception as error:
            self.reply(502, {'error': str(error)})

    def reply(self, status, data):
        content = json.dumps(data, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json; charset=utf-8')
        self.send_header('Cache-Control', 'no-store')
        self.end_headers()
        self.wfile.write(content)


if __name__ == '__main__':
    print('HLS demo: http://127.0.0.1:8766', flush=True)
    ThreadingHTTPServer(('127.0.0.1', 8766), Handler).serve_forever()
