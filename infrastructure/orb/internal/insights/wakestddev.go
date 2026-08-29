package insights

// wakeStdDevPercentile maps a wake-time standard deviation in minutes to the
// percentile of Sense users who were less consistent than that.
//
// Generated from the reference's WakeStdDevData, which embeds a CSV of the real
// distribution measured on 2015-07-25:
// https://s3.amazonaws.com/hello-data/insights-raw-data/wakeStdDev_distribution_2015_07_25.csv
//
// The values are TRUNCATED, not rounded: the reference parses each row with
// `(int) Float.parseFloat(...)`, so 2.8 becomes 2. Rounding instead shifts much
// of the table by one and changes the number shown to the user.
//
// Indexed by minute, 0 to 168. Anything at or above 169 is the 99th percentile.
var wakeStdDevPercentile = [...]int{
	0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 3, 3,
	3, 4, 4, 4, 5, 5, 6, 6, 6, 7, 7, 8, 8,
	9, 9, 10, 10, 11, 11, 12, 12, 13, 14, 14, 15, 16,
	16, 17, 18, 18, 19, 20, 20, 21, 22, 23, 23, 24, 25,
	26, 27, 27, 28, 29, 30, 31, 32, 32, 33, 34, 35, 36,
	37, 38, 39, 40, 41, 41, 42, 43, 44, 45, 46, 47, 48,
	49, 50, 51, 52, 53, 54, 55, 55, 56, 57, 58, 59, 60,
	61, 62, 63, 64, 64, 65, 66, 67, 68, 69, 70, 70, 71,
	72, 73, 73, 74, 75, 76, 76, 77, 78, 78, 79, 80, 80,
	81, 82, 82, 83, 84, 84, 85, 85, 86, 86, 87, 87, 88,
	88, 89, 89, 90, 90, 90, 91, 91, 92, 92, 92, 93, 93,
	93, 94, 94, 94, 94, 95, 95, 95, 96, 96, 96, 96, 96,
	97, 97, 97, 97, 97, 98, 98, 98, 98, 98, 98, 98, 98,
}

const (
	maxWakeStdDev           = 169
	maxWakeStdDevPercentile = 99
)

// percentileFor returns how many Sense users woke less consistently than this.
func percentileFor(stdDevMinutes int) int {
	if stdDevMinutes >= maxWakeStdDev {
		return maxWakeStdDevPercentile
	}
	if stdDevMinutes < 0 {
		return 0
	}
	return wakeStdDevPercentile[stdDevMinutes]
}
