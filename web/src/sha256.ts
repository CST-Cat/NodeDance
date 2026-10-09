// Incremental SHA-256 for browser Blob/File streams. It retains only one
// 64-byte block and the current stream chunk, so large uploads are not copied
// into a single ArrayBuffer just to establish their idempotency identity.
const roundConstants = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
])

function rotateRight(value: number, bits: number) {
  return (value >>> bits) | (value << (32 - bits))
}

class SHA256 {
  private readonly state = new Uint32Array([
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
    0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
  ])
  private readonly block = new Uint8Array(64)
  private readonly schedule = new Uint32Array(64)
  private blockLength = 0
  private bytesHashed = 0
  private finished = false

  update(input: Uint8Array) {
    if (this.finished) throw new Error('SHA-256 digest has already been finalized')
    this.bytesHashed += input.byteLength
    let offset = 0

    if (this.blockLength > 0) {
      const count = Math.min(64 - this.blockLength, input.byteLength)
      this.block.set(input.subarray(0, count), this.blockLength)
      this.blockLength += count
      offset += count
      if (this.blockLength === 64) {
        this.compress(this.block)
        this.blockLength = 0
      }
    }

    while (offset + 64 <= input.byteLength) {
      this.compress(input.subarray(offset, offset + 64))
      offset += 64
    }
    if (offset < input.byteLength) {
      this.block.set(input.subarray(offset), 0)
      this.blockLength = input.byteLength - offset
    }
  }

  digestHex() {
    if (this.finished) throw new Error('SHA-256 digest has already been finalized')
    this.finished = true
    const bitLength = this.bytesHashed * 8
    this.block[this.blockLength++] = 0x80
    if (this.blockLength > 56) {
      this.block.fill(0, this.blockLength)
      this.compress(this.block)
      this.blockLength = 0
    }
    this.block.fill(0, this.blockLength, 56)
    const view = new DataView(this.block.buffer)
    view.setUint32(56, Math.floor(bitLength / 0x1_0000_0000), false)
    view.setUint32(60, bitLength >>> 0, false)
    this.compress(this.block)
    return Array.from(this.state, word => word.toString(16).padStart(8, '0')).join('')
  }

  private compress(block: Uint8Array) {
    const view = new DataView(block.buffer, block.byteOffset, block.byteLength)
    for (let index = 0; index < 16; index++) this.schedule[index] = view.getUint32(index * 4, false)
    for (let index = 16; index < 64; index++) {
      const x = this.schedule[index - 15]
      const y = this.schedule[index - 2]
      const sigma0 = rotateRight(x, 7) ^ rotateRight(x, 18) ^ (x >>> 3)
      const sigma1 = rotateRight(y, 17) ^ rotateRight(y, 19) ^ (y >>> 10)
      this.schedule[index] = (this.schedule[index - 16] + sigma0 + this.schedule[index - 7] + sigma1) >>> 0
    }

    let a = this.state[0], b = this.state[1], c = this.state[2], d = this.state[3]
    let e = this.state[4], f = this.state[5], g = this.state[6], h = this.state[7]
    for (let index = 0; index < 64; index++) {
      const sum1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25)
      const choice = (e & f) ^ (~e & g)
      const temp1 = (h + sum1 + choice + roundConstants[index] + this.schedule[index]) >>> 0
      const sum0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22)
      const majority = (a & b) ^ (a & c) ^ (b & c)
      const temp2 = (sum0 + majority) >>> 0
      h = g; g = f; f = e; e = (d + temp1) >>> 0
      d = c; c = b; b = a; a = (temp1 + temp2) >>> 0
    }
    this.state[0] = (this.state[0] + a) >>> 0
    this.state[1] = (this.state[1] + b) >>> 0
    this.state[2] = (this.state[2] + c) >>> 0
    this.state[3] = (this.state[3] + d) >>> 0
    this.state[4] = (this.state[4] + e) >>> 0
    this.state[5] = (this.state[5] + f) >>> 0
    this.state[6] = (this.state[6] + g) >>> 0
    this.state[7] = (this.state[7] + h) >>> 0
  }
}

export async function sha256Stream(stream: ReadableStream<Uint8Array>): Promise<string> {
  const hash = new SHA256()
  const reader = stream.getReader()
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      hash.update(value)
    }
    return hash.digestHex()
  } finally {
    reader.releaseLock()
  }
}

export function sha256Blob(blob: Blob): Promise<string> {
  return sha256Stream(blob.stream())
}
