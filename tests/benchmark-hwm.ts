import { createReadStream } from 'node:fs'
import { writeFile, unlink } from 'node:fs/promises'
import { randomBytes } from 'node:crypto'

const HIGH_WATER_MARKS = [
  { name: '64KB (default)', value: 64 * 1024 },
  { name: '128KB', value: 128 * 1024 },
  { name: '256KB (current)', value: 256 * 1024 },
  { name: '512KB', value: 512 * 1024 },
  { name: '1MB', value: 1024 * 1024 },
]

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
    console.log('Buffer Size'.padEnd(20) + 'Throughput (MB/s)'.padEnd(20) + 'Avg Duration (ms)')
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
      const throughput = (fileSize.bytes / (1024 * 1024)) / (avgDuration / 1000)

      console.log(
        hwm.name.padEnd(20) +
        throughput.toFixed(2).padEnd(20) +
        avgDuration.toFixed(2)
      )
    }

    // Cleanup
    await unlink(testFile)
  }
}

benchmark().catch(console.error)
