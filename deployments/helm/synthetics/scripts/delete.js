import storj from 'k6/x/storj';
import { check } from 'k6';
import { Counter, Rate } from 'k6/metrics';

// Custom metrics for delete operations
const deleteSuccess = new Rate('storj_delete_success');
const deleteCount = new Counter('storj_delete_count_total');

export const options = {
    vus: 1,
    iterations: 1,
    thresholds: {
        'storj_delete_success': ['rate>0.95'],
    },
};

export default function () {
    const accessGrant = __ENV.STORJ_ACCESS_GRANT;
    const bucketName = __ENV.STORJ_BUCKET || 'synthetics-test';
    const filePrefix = __ENV.FILE_PREFIX || 'synthetics';
    const maxAge = parseInt(__ENV.MAX_AGE_MINUTES || '60'); // Default: delete files older than 60 minutes
    const maxFiles = parseInt(__ENV.MAX_DELETE || '10'); // Default: max 10 files per run
    const specificFile = __ENV.FILE_NAME; // Optional: delete specific file
    const sharedFile = __ENV.SHARED_FILE; // For test groups (includes ULID)
    const testGroup = __ENV.TEST_GROUP; // Test group name
    const testULID = __ENV.TEST_ULID; // ULID for this test run

    if (!accessGrant) {
        console.error('STORJ_ACCESS_GRANT environment variable is required');
        return;
    }

    // Create Storj client
    const client = storj.newClient(accessGrant);

    try {
        // Priority: SHARED_FILE > FILE_NAME > cleanup by prefix
        const targetFile = sharedFile || specificFile;

        if (targetFile) {
            // Delete specific file
            if (testGroup && testULID) {
                console.log(`[Group: ${testGroup}] Deleting ULID-based file: ${sharedFile}`);
                console.log(`[Group: ${testGroup}] Test ULID: ${testULID}`);
            } else {
                console.log(`Deleting specific file: ${targetFile}`);
            }
            deleteFile(client, bucketName, targetFile);
        } else {
            // Delete old test files
            console.log(`Listing files with prefix: ${filePrefix}`);
            const files = client.list(bucketName);

            if (!files || files.length === 0) {
                console.log('No files found in bucket. Nothing to delete.');
                return;
            }

            // Filter files by prefix
            const matchingFiles = files.filter(f => f.startsWith(filePrefix));

            if (matchingFiles.length === 0) {
                console.log(`No files found with prefix: ${filePrefix}. Nothing to delete.`);
                return;
            }

            console.log(`Found ${matchingFiles.length} files with prefix: ${filePrefix}`);

            // Parse timestamps from filenames and filter by age
            const now = Date.now();
            const maxAgeMs = maxAge * 60 * 1000;
            let filesToDelete = [];

            for (const file of matchingFiles) {
                // Extract timestamp from filename (format: prefix-timestamp.bin)
                const match = file.match(/-(\d+)\.bin$/);
                if (match) {
                    const timestamp = parseInt(match[1]);
                    const age = now - timestamp;

                    if (age > maxAgeMs) {
                        filesToDelete.push({
                            name: file,
                            age: Math.floor(age / 1000 / 60), // age in minutes
                        });
                    }
                }
            }

            if (filesToDelete.length === 0) {
                console.log(`No files older than ${maxAge} minutes found. Nothing to delete.`);
                return;
            }

            // Sort by age (oldest first) and limit
            filesToDelete.sort((a, b) => b.age - a.age);
            filesToDelete = filesToDelete.slice(0, maxFiles);

            console.log(`Deleting ${filesToDelete.length} file(s) older than ${maxAge} minutes:`);

            let deletedCount = 0;
            for (const file of filesToDelete) {
                console.log(`  - ${file.name} (${file.age} minutes old)`);
                if (deleteFile(client, bucketName, file.name)) {
                    deletedCount++;
                }
            }

            console.log(`Successfully deleted ${deletedCount}/${filesToDelete.length} file(s)`);
        }

    } finally {
        // Always close the client
        try {
            client.close();
        } catch (err) {
            console.warn('Failed to close client:', err);
        }
    }
}

// Delete a single file and record metrics
function deleteFile(client, bucketName, fileName) {
    let deleteErr = null;
    try {
        client.delete(bucketName, fileName);
    } catch (err) {
        deleteErr = err;
        console.error(`Failed to delete ${fileName}:`, err);
    }

    deleteSuccess.add(deleteErr === null);
    if (deleteErr === null) {
        deleteCount.add(1);
    }

    check(deleteErr, {
        'delete succeeded': (err) => err === null,
    });

    return deleteErr === null;
}
