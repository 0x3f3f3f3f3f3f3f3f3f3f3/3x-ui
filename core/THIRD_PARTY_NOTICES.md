# Additional Custom Xray-core dependency notices

The imported Xray-core retains its own LICENSE and source notices. This file adds notices for new dependencies.

## AmneziaWG Go v3.1.20260828 (existing panel runtime)

Source: https://github.com/amnezia-vpn/amneziawg-go/tree/b5928efb6ca19f0153958460c3d141f04abc5c2e

The module source is retained under `deps/amneziawg-go`, with its complete
[MIT license](deps/amneziawg-go/LICENSE) and original file notices. Its immutable
origin and the timer synchronization / TUN padding patches are recorded in
[the source manifest](deps/amneziawg-go.UPSTREAM.json). The root module uses this
local source for reproducible builds. This dependency still serves the existing
panel-side AmneziaWG runtime; it has not yet been migrated into Custom Xray-core.

## bbolt v1.5.0

Source: https://github.com/etcd-io/bbolt/tree/v1.5.0

The MIT License (MIT)

Copyright (c) 2013 Ben Johnson

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
the Software, and to permit persons to whom the Software is furnished to do so,
subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.


## mieru v3.38.0

Copyright (C) 2024–2026 mieru authors. GPL-3.0-or-later.
Pinned source: https://github.com/enfein/mieru/tree/b961978c3be9dd26b94158487c760858e19d1db2

The official library is compiled into Custom Xray-core. Its original source
notices remain in the dependency; the complete license is retained at
[licenses/mieru-GPL-3.0.txt](licenses/mieru-GPL-3.0.txt). No external mieru server
process or local proxy bridge is used. Native adapter source and its pinned
wire/header dependencies are in core/xray/proxy/mieru. Build metadata retains
Custom Xray naming and the source revision. Release licensing review remains
part of the existing packaging gate.
