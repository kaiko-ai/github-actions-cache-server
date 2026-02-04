import { randomBytes } from 'node:crypto'
import { createReadStream } from 'node:fs'
import { unlink, writeFile } from 'node:fs/promises'

import { envBaseSchema } from '../lib/schemas.js'

// Get the default value from the schema
const optionals = envBaseSchema.structure?.inner?.optional || []
const hwmNode = optionals.find((n: { key: string }) => n.key === 'STORAGE_HIGH_WATER_MARK')
const CURRENT_HIGH_WATER_MARK = (hwmNode?.default as number) ?? 1024 * 1024

function formatSize(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${bytes / (1024 * 1024)}MB`
  return `${bytes / 1024}KB`
}

function getLabel(value: number): string {
  const size = formatSize(value)
  if (value === 64 * 1024) return `${size} (Node.js default)`
  if (value === CURRENT_HIGH_WATER_MARK) return `${size} (current)`
  return size
}

const HIGH_WATER_MARKS = [
  { value: 64 * 1024 },
  { value: 128 * 1024 },
  { value: 256 * 1024 },
  { value: 512 * 1024 },
  { value: 1024 * 1024 },
].map((h) => ({ name: getLabel(h.value), value: h.value }))

const FILE_SIZES = [
  { name: '100MB', bytes: 100 * 1024 * 1024 },
  { name: '500MB', bytes: 500 * 1024 * 1024 },
]

const ITERATIONS = 3

async function benchmark() {
  for (const fileSize of FILE_SIZES) {
    // Create test file
    const testFile = `/tmp/benchmark-${fileSize.name}.bin`
    console.log(`\nCreating ${fileSize.name} test file...`)
    await writeFile(testFile, randomBytes(fileSize.bytes))

    console.log(`\n=== ${fileSize.name} File ===`)
    console.log(`${'Buffer Size'.padEnd(20)}${'Throughput (MB/s)'.padEnd(20)}Avg Duration (ms)`)
    console.log('-'.repeat(60))

    for (const hwm of HIGH_WATER_MARKS) {
      const durations: number[] = []

      for (let i = 0; i < ITERATIONS; i++) {
        const start = performance.now()
        await new Promise<void>((resolve, reject) => {
          const stream = createReadStream(testFile, { highWaterMark: hwm.value })
          stream.on('data', () => {}) // Consume data
          stream.on('end', resolve)
          stream.on('error', reject)
        })
        durations.push(performance.now() - start)
      }

      const avgDuration = durations.reduce((a, b) => a + b) / durations.length
      const throughput = fileSize.bytes / (1024 * 1024) / (avgDuration / 1000)

      console.log(
        `${hwm.name.padEnd(20)}${throughput.toFixed(2).padEnd(20)}${avgDuration.toFixed(2)}`,
      )
    }

    // Cleanup
    await unlink(testFile)
  }
}

await benchmark()
