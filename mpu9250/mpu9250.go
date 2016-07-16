package mpu9250

// Approach adapted from the InvenSense DMP 6.1 drivers
// Also referenced https://github.com/brianc118/MPU9250/blob/master/MPU9250.cpp

import (
	"fmt"
	"github.com/kidoman/embd"
	_ "github.com/kidoman/embd/host/all"
	_ "github.com/kidoman/embd/host/rpi"

	"log"
	"math"
	"time"
	"errors"
	"sync"
)

type MPU9250 struct {
	i2cbus                embd.I2CBus
	scaleGyro, scaleAccel float64 // Max sensor reading for value 2**15-1
	sampleRate 	      int
	n, nm                 float64 // Number of samples taken since last read
	g1, g2, g3            int32 // Gyro accumulated values, rad/s
	a1, a2, a3            int32 // Accel accumulated values, G
	m1, m2, m3            int32 // Magnetometer accumulated values, uT
	a01, a02, a03	      int16 // Accelerometer bias
	g01, g02, g03	      int16 // Gyro bias
	mcal1, mcal2, mcal3   int32 // Magnetometer calibration values, uT
	m 			sync.Mutex
}

func NewMPU9250(sensitivityGyro, sensitivityAccel, sampleRate int, applyHWOffsets bool) (*MPU9250, error) {
	var mpu = new(MPU9250)
	var sensGyro, sensAccel byte
	mpu.sampleRate = sampleRate

	switch {
	case sensitivityGyro>1000:
		sensGyro = BITS_FS_2000DPS
		mpu.scaleGyro = 2000.0 / float64(math.MaxInt16)
	case sensitivityGyro>500:
		sensGyro = BITS_FS_1000DPS
		mpu.scaleGyro = 1000.0 / float64(math.MaxInt16)
	case sensitivityGyro>250:
		sensGyro = BITS_FS_500DPS
		mpu.scaleGyro = 500.0 / float64(math.MaxInt16)
	default:
		sensGyro = BITS_FS_250DPS
		mpu.scaleGyro = 250.0 / float64(math.MaxInt16)
	}

	switch {
	case sensitivityAccel>8:
		sensAccel = BITS_FS_16G
		mpu.scaleAccel = 16.0 / float64(math.MaxInt16)
	case sensitivityAccel>4:
		sensAccel = BITS_FS_8G
		mpu.scaleAccel = 8.0 / float64(math.MaxInt16)
	case sensitivityAccel>2:
		sensAccel = BITS_FS_4G
		mpu.scaleAccel = 4.0 / float64(math.MaxInt16)
	default:
		sensAccel = BITS_FS_2G
		mpu.scaleAccel = 2.0 / float64(math.MaxInt16)
	}

	mpu.i2cbus = embd.NewI2CBus(1)

	// Initialization of MPU
	// Reset Device
	if err := mpu.i2cWrite(MPUREG_PWR_MGMT_1, BIT_H_RESET); err != nil {
		return nil, errors.New("Error resetting MPU9250")
	}
	time.Sleep(100*time.Millisecond)		// As in inv_mpu
	// Wake device
	if err := mpu.i2cWrite(MPUREG_PWR_MGMT_1, 0x00); err != nil {
		return nil, errors.New("Error waking MPU9250")
	}
	// Don't let FIFO overwrite DMP data
	if err := mpu.i2cWrite(MPUREG_ACCEL_CONFIG_2, BIT_FIFO_SIZE_1024 | 0x8); err != nil {
		return nil, errors.New("Error setting up MPU9250")
	}

	/*
	// Invalidate some registers
	// This is done in DMP C drivers, not sure it's needed here
	// Matches gyro_cfg >> 3 & 0x03
	unsigned char gyro_fsr;
	// Matches accel_cfg >> 3 & 0x03
	unsigned char accel_fsr;
	// Enabled sensors. Uses same masks as fifo_en, NOT pwr_mgmt_2.
	unsigned char sensors;
	// Matches config register.
	unsigned char lpf;
	unsigned char clk_src;
	// Sample rate, NOT rate divider.
	unsigned short sample_rate;
	// Matches fifo_en register.
	unsigned char fifo_enable;
	// Matches int enable register.
	unsigned char int_enable;
	// 1 if devices on auxiliary I2C bus appear on the primary.
	unsigned char bypass_mode;
	// 1 if half-sensitivity.
	// NOTE: This doesn't belong here, but everything else in hw_s is const,
	// and this allows us to save some precious RAM.
	 //
	unsigned char accel_half;
	// 1 if device in low-power accel-only mode.
	unsigned char lp_accel_mode;
	// 1 if interrupts are only triggered on motion events.
	unsigned char int_motion_only;
	struct motion_int_cache_s cache;
	// 1 for active low interrupts.
	unsigned char active_low_int;
	// 1 for latched interrupts.
	unsigned char latched_int;
	// 1 if DMP is enabled.
	unsigned char dmp_on;
	// Ensures that DMP will only be loaded once.
	unsigned char dmp_loaded;
	// Sampling rate used when DMP is enabled.
	unsigned short dmp_sample_rate;
	// Compass sample rate.
	unsigned short compass_sample_rate;
	unsigned char compass_addr;
	short mag_sens_adj[3];
	*/

	// Set Gyro and Accel sensitivities
	if err := mpu.i2cWrite(MPUREG_GYRO_CONFIG, sensGyro); err != nil {
		return nil, errors.New("Error setting MPU9250 gyro sensitivity")
	}
	if err := mpu.i2cWrite(MPUREG_ACCEL_CONFIG, sensAccel); err != nil {
		return nil, errors.New("Error setting MPU9250 accel sensitivity")
	}
	sampRate := byte(1000/mpu.sampleRate-1)
	// Set LPF to half of sample rate
	if err := mpu.SetLPF(sampRate >> 1); err != nil {
		return err
	}
	// Set sample rate to chosen
	if err := mpu.SetSampleRate(sampRate); err != nil {
		return err
	}
	// Turn off FIFO buffer
	if err := mpu.i2cWrite(MPUREG_INT_ENABLE, 0x00); err != nil {
		return nil, errors.New("Error setting up MPU9250")
	}
	// Turn off FIFO buffer
	//mpu.i2cWrite(MPUREG_FIFO_EN, 0x00)

	// Set up magnetometer
	if USEMAG {
		if err := mpu.ReadMagCalibration(); err != nil {
			return nil, errors.New("Error reading calibration from magnetometer")
		}

		// Set up AK8963 master mode, master clock and ES bit
		if err := mpu.i2cWrite(MPUREG_I2C_MST_CTRL, 0x40); err != nil {
			return nil, errors.New("Error setting up AK8963")
		}
		// Slave 0 reads from AK8963
		if err := mpu.i2cWrite(MPUREG_I2C_SLV0_ADDR, BIT_I2C_READ | AK8963_I2C_ADDR); err != nil {
			return nil, errors.New("Error setting up AK8963")
		}
		// Compass reads start at this register
		if err := mpu.i2cWrite(MPUREG_I2C_SLV0_REG, AK8963_ST1); err != nil {
			return nil, errors.New("Error setting up AK8963")
		}
		// Enable 8-byte reads on slave 0
		if err := mpu.i2cWrite(MPUREG_I2C_SLV0_CTRL, BIT_SLAVE_EN | 8); err != nil {
			return nil, errors.New("Error setting up AK8963")
		}
		// Slave 1 can change AK8963 measurement mode
		if err := mpu.i2cWrite(MPUREG_I2C_SLV1_ADDR, AK8963_I2C_ADDR); err != nil {
			return nil, errors.New("Error setting up AK8963")
		}
		if err := mpu.i2cWrite(MPUREG_I2C_SLV1_REG, AK8963_CNTL1); err != nil {
			return nil, errors.New("Error setting up AK8963")
		}
		// Enable 1-byte reads on slave 1
		if err := mpu.i2cWrite(MPUREG_I2C_SLV1_CTRL, BIT_SLAVE_EN | 1); err != nil {
			return nil, errors.New("Error setting up AK8963")
		}
		// Set slave 1 data
		if err := mpu.i2cWrite(MPUREG_I2C_SLV1_DO, AKM_SINGLE_MEASUREMENT); err != nil {
			return nil, errors.New("Error setting up AK8963")
		}
		// Triggers slave 0 and 1 actions at each sample
		if err := mpu.i2cWrite(MPUREG_I2C_MST_DELAY_CTRL, 0x03); err != nil {
			return nil, errors.New("Error setting up AK8963")
		}

		// Set AK8963 sample rate to same as gyro/accel sample rate, up to max
		var ak8963Rate byte
		if mpu.sampleRate < AK8963_MAX_SAMPLE_RATE {
			ak8963Rate = 0
		} else {
			ak8963Rate = byte(mpu.sampleRate / AK8963_MAX_SAMPLE_RATE - 1)
		}
		// Not so sure of this one--I2C Slave 4??!
		if err := mpu.i2cWrite(MPUREG_I2C_SLV4_CTRL, ak8963Rate); err != nil {
			return nil, errors.New("Error setting up AK8963")
		}
	}

	// Set clock source to PLL
	if err := mpu.i2cWrite(MPUREG_PWR_MGMT_1, INV_CLK_PLL); err != nil {
		return nil, errors.New("Error setting up MPU9250")
	}
	// Turn off all sensors -- Not sure if necessary, but it's in the InvenSense DMP driver
	if err := mpu.i2cWrite(MPUREG_PWR_MGMT_2, 0x63); err != nil {
		return nil, errors.New("Error setting up MPU9250")
	}
	time.Sleep(5 * time.Millisecond)
	// Turn on all gyro, all accel
	if err := mpu.i2cWrite(MPUREG_PWR_MGMT_2, 0x00); err != nil {
		return nil, errors.New("Error setting up MPU9250")
	}

	if applyHWOffsets {
		if err := mpu.ReadAccelBias(sensAccel); err != nil {
			return nil, err
		}
		if err := mpu.ReadGyroBias(sensGyro); err != nil {
			return nil, err
		}
	}

	// Usually we don't want the automatic gyro bias compensation - it pollutes the gyro in a non-inertial frame
	if err := mpu.EnableGyroBiasCal(false); err != nil {
		return nil, err
	}

	go mpu.readMPURaw()

	time.Sleep(100 * time.Millisecond) // Make sure it's ready
	return mpu, nil
}

// readMPURaw reads all sensors and totals the values and number of samples
// When Read is called, we will return the averages
func (m *MPU9250) readMPURaw() {
	var g1, g2, g3, a1, a2, a3, m1, m2, m3, m4 int16
	var err error

	clock := time.NewTicker(time.Duration(int(1000.0/float32(m.sampleRate)+0.5)) * time.Millisecond)

	for {
		<-clock.C
		m.m.Lock() //TODO: There must be a better way using channels
		// Read gyro data:
		g1, err = m.i2cRead2(MPUREG_GYRO_XOUT_H)
		if err != nil {
			log.Println("MPU9250 Warning: error reading gyro")
			goto readMagData
		}
		g2, err = m.i2cRead2(MPUREG_GYRO_YOUT_H)
		if err != nil {
			log.Println("MPU9250 Warning: error reading gyro")
			goto readMagData
		}
		g3, err = m.i2cRead2(MPUREG_GYRO_ZOUT_H)
		if err != nil {
			log.Println("MPU9250 Warning: error reading gyro")
			goto readMagData
		}

		// Read accelerometer data:
		a1, err = m.i2cRead2(MPUREG_ACCEL_XOUT_H)
		if err != nil {
			log.Println("MPU9250 Warning: error reading accelerometer")
			goto readMagData
		}
		a2, err = m.i2cRead2(MPUREG_ACCEL_YOUT_H)
		if err != nil {
			log.Println("MPU9250 Warning: error reading accelerometer")
			goto readMagData
		}
		a3, err = m.i2cRead2(MPUREG_ACCEL_ZOUT_H)
		if err != nil {
			log.Println("MPU9250 Warning: error reading accelerometer")
			goto readMagData
		}

		// Update values and increment count of gyro/accel readings
		m.g1 += int32(g1 - m.g01)
		m.g2 += int32(g2 - m.g02)
		m.g3 += int32(g3 - m.g03)
		m.a1 += int32(a1 - m.a01)
		m.a2 += int32(a2 - m.a02)
		m.a3 += int32(a3 - m.a03)
		m.n += 1.0

		readMagData:
		if USEMAG {
			// Read magnetometer data:
			m.i2cWrite(MPUREG_I2C_SLV0_ADDR, AK8963_I2C_ADDR | READ_FLAG)
			m.i2cWrite(MPUREG_I2C_SLV0_REG, AK8963_HXL) //I2C slave 0 register address from where to begin data transfer
			m.i2cWrite(MPUREG_I2C_SLV0_CTRL, 0x87)      //Read 7 bytes from the magnetometer

			m1, err = m.i2cRead2(MPUREG_EXT_SENS_DATA_00)
			if err != nil {
				log.Println("MPU9250 Warning: error reading magnetometer")
				return        // Don't update the accumulated values
			}
			m2, err = m.i2cRead2(MPUREG_EXT_SENS_DATA_02)
			if err != nil {
				log.Println("MPU9250 Warning: error reading magnetometer")
				return        // Don't update the accumulated values
			}
			m3, err = m.i2cRead2(MPUREG_EXT_SENS_DATA_04)
			if err != nil {
				log.Println("MPU9250 Warning: error reading magnetometer")
				return        // Don't update the accumulated values
			}
			m4, err = m.i2cRead2(MPUREG_EXT_SENS_DATA_06)
			if err != nil {
				log.Println("MPU9250 Warning: error reading magnetometer")
				return        // Don't update the accumulated values
			}

			if (byte(m1 & 0xFF) & AKM_DATA_READY) == 0x00 && (byte(m1 & 0xFF) & AKM_DATA_OVERRUN) != 0x00 {
				log.Println("MPU9250 Mag data not ready or overflow")
				log.Printf("MPU9250 m1 LSB: %X\n", byte(m1 & 0xFF))
				return        // Don't update the accumulated values
			}

			if (byte((m4 >> 8) & 0xFF) & AKM_OVERFLOW) != 0x00 {
				log.Println("MPU9250 Mag data overflow")
				log.Printf("MPU9250 m4 MSB: %X\n", byte((m1 >> 8) & 0xFF))
				return        // Don't update the accumulated values
			}

			m.m1 += (int32(m1) * m.mcal1 >> 8)
			m.m2 += (int32(m2) * m.mcal2 >> 8)
			m.m3 += (int32(m3) * m.mcal3 >> 8)

			m.nm += 1.0
		}
		m.m.Unlock()
	}
}

func (m *MPU9250) Read() (t int64, g1, g2, g3, a1, a2, a3, m1, m2, m3 float64, gaError, magError error) {
	m.m.Lock()

	if m.n > 0 {
		g1, g2, g3 = float64(m.g1) / m.n * m.scaleGyro, float64(m.g2) / m.n * m.scaleGyro, float64(m.g3) / m.n * m.scaleGyro
		a1, a2, a3 = float64(m.a1) / m.n * m.scaleAccel, float64(m.a2) / m.n * m.scaleAccel, float64(m.a3) / m.n * m.scaleAccel
		gaError = nil
	} else {
		log.Printf("MPU error: %2.f values accumulated\n", m.n)
		gaError = errors.New("MPU9250 Read: error reading gyro/accel")
	}
	if m.nm > 0 {
		m1, m2, m3 = float64(m.m1) / m.nm, float64(m.m2) / m.nm, float64(m.m3) / m.nm
		magError = nil
	} else if USEMAG {
		magError = errors.New("MPU9250 Read: error reading magnetometer")
	} else {
		magError = nil
	}
	t = time.Now().UnixNano()

	m.g1, m.g2, m.g3 = 0, 0, 0
	m.a1, m.a2, m.a3 = 0, 0, 0
	m.m1, m.m2, m.m3 = 0, 0, 0
	m.n, m.nm = 0, 0
	m.m.Unlock()

	return
}

func (m *MPU9250) CloseMPU() {
	return // Nothing to do for the 9250?
}

// CalibrateGyro does a live calibration of the gyro, sampling over (dur int) seconds.
// It is only intended to be run intelligently so that it isn't called when the sensor is in a non-inertial state.
func (m *MPU9250) Calibrate(dur int) error {
	var (
		n int32 = int32(dur)*int32(m.sampleRate)
		g11, g12, g13 int32	// Accumulators for calculating mean drifts
		g21, g22, g23 int64	// Accumulators for calculating stdev drifts
		a11, a12, a13 int32	// Accumulators for calculating mean drifts
		a21, a22, a23 int64	// Accumulators for calculating stdev drifts
	)

	clock := time.NewTicker(time.Duration(int(1000.0/float32(m.sampleRate)+0.5)) * time.Millisecond)
	m.m.Lock()

	for i := int32(0); i<n; i++ {
		<- clock.C

		g1, err := m.i2cRead2(MPUREG_GYRO_XOUT_H)
		if err != nil {
			return errors.New("CalibrationGyro: sensor error during calibration")
		}
		g2, err := m.i2cRead2(MPUREG_GYRO_YOUT_H)
		if err != nil {
			return errors.New("CalibrationGyro: sensor error during calibration")
		}
		g3, err := m.i2cRead2(MPUREG_GYRO_ZOUT_H)
		if err != nil {
			return errors.New("CalibrationGyro: sensor error during calibration")
		}
		g11 += int32(g1)
		g12 += int32(g2)
		g13 += int32(g3)
		g21 += int64(g1)*int64(g1)
		g22 += int64(g2)*int64(g2)
		g23 += int64(g3)*int64(g3)

		a1, err := m.i2cRead2(MPUREG_ACCEL_XOUT_H)
		if err != nil {
			return errors.New("CalibrationAccel: sensor error during calibration")
		}
		a2, err := m.i2cRead2(MPUREG_ACCEL_YOUT_H)
		if err != nil {
			return errors.New("CalibrationAccel: sensor error during calibration")
		}
		a3, err := m.i2cRead2(MPUREG_ACCEL_ZOUT_H)
		if err != nil {
			return errors.New("CalibrationAccel: sensor error during calibration")
		}
		a3 -= int16(1.0/m.scaleAccel)
		a11 += int32(a1)
		a12 += int32(a2)
		a13 += int32(a3)
		a21 += int64(a1)*int64(a1)
		a22 += int64(a2)*int64(a2)
		a23 += int64(a3)*int64(a3)
	}
	clock.Stop()

	// Too much variance in the gyro readings means it was moving too much for a good calibration
	log.Printf("MPU9250 gyro calibration variance: %f %f %f\n", (float64(g21-int64(g11)*int64(g11)/int64(n))*m.scaleGyro*m.scaleGyro/float64(n)),
		(float64(g22-int64(g12)*int64(g12)/int64(n))*m.scaleGyro*m.scaleGyro/float64(n)),
		(float64(g23-int64(g13)*int64(g13)/int64(n))*m.scaleGyro*m.scaleGyro/float64(n)))
	log.Printf("MPU9250 accel calibration variance: %f %f %f\n", (float64(a21-int64(a11)*int64(a11)/int64(n))*m.scaleAccel*m.scaleAccel/float64(n)),
		(float64(a22-int64(a12)*int64(a12)/int64(n))*m.scaleAccel*m.scaleAccel/float64(n)),
		(float64(a23-int64(a13)*int64(a13)/int64(n))*m.scaleAccel*m.scaleAccel/float64(n)))
	if (float64(g21-int64(g11)*int64(g11)/int64(n))*m.scaleGyro*m.scaleGyro > float64(n)* MAXGYROVAR) ||
		(float64(g22-int64(g12)*int64(g12)/int64(n))*m.scaleGyro*m.scaleGyro > float64(n)* MAXGYROVAR) ||
		(float64(g23-int64(g13)*int64(g13)/int64(n))*m.scaleGyro*m.scaleGyro > float64(n)* MAXGYROVAR) ||
		(float64(a21-int64(a11)*int64(a11)/int64(n))*m.scaleAccel*m.scaleAccel > float64(n)* MAXACCELVAR) ||
		(float64(a22-int64(a12)*int64(a12)/int64(n))*m.scaleAccel*m.scaleAccel > float64(n)* MAXACCELVAR) ||
		(float64(a23-int64(a13)*int64(a13)/int64(n))*m.scaleAccel*m.scaleAccel > float64(n)* MAXACCELVAR) {
		return errors.New("MPU9250 CalibrationAll: sensor was not inertial during calibration")
	}


	m.g01 = int16(g11/n)
	m.g02 = int16(g12/n)
	m.g03 = int16(g13/n)
	m.a01 = int16(a11/n)
	m.a02 = int16(a12/n)
	m.a03 = int16(a13/n)


	m.m.Unlock()
	log.Printf("MPU9250 Gyro calibration: %d, %d, %d\n", m.g01, m.g02, m.g03)
	log.Printf("MPU9250 Accel calibration: %d, %d, %d\n", m.a01, m.a02, m.a03)
	return nil
}

// CalibrateGyro does a live calibration of the gyro, sampling over (dur int) seconds.
// It is only intended to be run intelligently so that it isn't called when the sensor is in a non-inertial state.
func (m *MPU9250) CalibrateGyro(dur int) error {
	const maxVar = 10.0
	var (
		n int32 = int32(dur)*int32(m.sampleRate)
		g11, g12, g13 int32	// Accumulators for calculating mean drifts
		g21, g22, g23 int64	// Accumulators for calculating stdev drifts
	)

	clock := time.NewTicker(time.Duration(int(1000.0/float32(m.sampleRate)+0.5)) * time.Millisecond)
	m.m.Lock()

	for i := int32(0); i<n; i++ {
		<- clock.C

		g1, err := m.i2cRead2(MPUREG_GYRO_XOUT_H)
		if err != nil {
			return errors.New("CalibrationGyro: sensor error during calibration")
		}
		g2, err := m.i2cRead2(MPUREG_GYRO_YOUT_H)
		if err != nil {
			return errors.New("CalibrationGyro: sensor error during calibration")
		}
		g3, err := m.i2cRead2(MPUREG_GYRO_ZOUT_H)
		if err != nil {
			return errors.New("CalibrationGyro: sensor error during calibration")
		}
		g11 += int32(g1)
		g12 += int32(g2)
		g13 += int32(g3)
		g21 += int64(g1)*int64(g1)
		g22 += int64(g2)*int64(g2)
		g23 += int64(g3)*int64(g3)
	}
	clock.Stop()

	// Too much variance in the gyro readings means it was moving too much for a good calibration
	log.Printf("MPU9250 calibration variance: %f %f %f\n", (float64(g21-int64(g11)*int64(g11)/int64(n))*m.scaleGyro*m.scaleGyro/float64(n)),
		(float64(g22-int64(g12)*int64(g12)/int64(n))*m.scaleGyro*m.scaleGyro/float64(n)),
		(float64(g23-int64(g13)*int64(g13)/int64(n))*m.scaleGyro*m.scaleGyro/float64(n)))
	if (float64(g21-int64(g11)*int64(g11)/int64(n))*m.scaleGyro*m.scaleGyro > float64(n)*maxVar) ||
		(float64(g22-int64(g12)*int64(g12)/int64(n))*m.scaleGyro*m.scaleGyro > float64(n)*maxVar) ||
		(float64(g23-int64(g13)*int64(g13)/int64(n))*m.scaleGyro*m.scaleGyro > float64(n)*maxVar) {
		return errors.New("CalibrationGyro: sensor was not inertial during calibration")
	}

	m.g01 = int16(g11/n)
	m.g02 = int16(g12/n)
	m.g03 = int16(g13/n)

	m.m.Unlock()
	log.Printf("MPU9250 Gyro calibration: %d, %d, %d\n", m.g01, m.g02, m.g03)
	return nil
}

// CalibrateAccel does a live calibration of the accelerometer, sampling over (dur int) seconds.
// It is only intended to be run intelligently so that it isn't called when the sensor is in a non-inertial state.
// It assumes that the gyro is level, so it should only be feeling 1G in the z direction
func (m *MPU9250) CalibrateAccel(dur int) error {
	const maxVar = 10.0
	var (
		n int32 = int32(dur)*int32(m.sampleRate)
		a11, a12, a13 int32	// Accumulators for calculating mean drifts
		a21, a22, a23 int64	// Accumulators for calculating stdev drifts
	)

	clock := time.NewTicker(time.Duration(int(1000.0/float32(m.sampleRate)+0.5)) * time.Millisecond)
	m.m.Lock()

	for i := int32(0); i<n; i++ {
		<- clock.C

		a1, err := m.i2cRead2(MPUREG_ACCEL_XOUT_H)
		if err != nil {
			return errors.New("CalibrationAccel: sensor error during calibration")
		}
		a2, err := m.i2cRead2(MPUREG_ACCEL_YOUT_H)
		if err != nil {
			return errors.New("CalibrationAccel: sensor error during calibration")
		}
		a3, err := m.i2cRead2(MPUREG_ACCEL_ZOUT_H)
		if err != nil {
			return errors.New("CalibrationAccel: sensor error during calibration")
		}
		a3 -= int16(1.0/m.scaleAccel)
		a11 += int32(a1)
		a12 += int32(a2)
		a13 += int32(a3)
		a21 += int64(a1)*int64(a1)
		a22 += int64(a2)*int64(a2)
		a23 += int64(a3)*int64(a3)
	}
	clock.Stop()

	// Too much variance in the accel readings means it was moving too much for a good calibration
	log.Printf("MPU9250 calibration variance: %f %f %f\n", (float64(a21-int64(a11)*int64(a11)/int64(n))*m.scaleAccel*m.scaleAccel/float64(n)),
		(float64(a22-int64(a12)*int64(a12)/int64(n))*m.scaleAccel*m.scaleAccel/float64(n)),
		(float64(a23-int64(a13)*int64(a13)/int64(n))*m.scaleAccel*m.scaleAccel/float64(n)))
	if (float64(a21-int64(a11)*int64(a11)/int64(n))*m.scaleAccel*m.scaleAccel > float64(n)*maxVar) ||
		(float64(a22-int64(a12)*int64(a12)/int64(n))*m.scaleAccel*m.scaleAccel > float64(n)*maxVar) ||
		(float64(a23-int64(a13)*int64(a13)/int64(n))*m.scaleAccel*m.scaleAccel > float64(n)*maxVar) {
		return errors.New("CalibrationAccel: sensor was not inertial during calibration")
	}

	m.a01 = int16(a11/n)
	m.a02 = int16(a12/n)
	m.a03 = int16(a13/n)

	m.m.Unlock()
	log.Printf("MPU9250 Accel calibration: %d, %d, %d\n", m.a01, m.a02, m.a03)
	return nil
}

func (mpu *MPU9250) SetSampleRate(rate byte) (err error) {
	errWrite := mpu.i2cWrite(MPUREG_SMPLRT_DIV, byte(rate)) // Set sample rate to chosen
	if errWrite != nil {
		err = fmt.Errorf("MPU9250 Error: Couldn't set sample rate: %s", errWrite.Error())
	}
	return
}

func (mpu*MPU9250) SetLPF(rate byte) (err error) {
	var r byte
	switch {
	case rate >= 188:
		r = BITS_DLPF_CFG_188HZ
	case rate >= 98:
		r = BITS_DLPF_CFG_98HZ
	case rate >= 42:
		r = BITS_DLPF_CFG_42HZ;
	case rate >= 20:
		r = BITS_DLPF_CFG_20HZ
	case rate >= 10:
		r = BITS_DLPF_CFG_10HZ
	default:
		r = BITS_DLPF_CFG_5HZ
	}

	errWrite := mpu.i2cWrite(MPUREG_CONFIG, r)
	if errWrite != nil {
		err = fmt.Errorf("MPU9250 Error: couldn't set LPF: %s", errWrite.Error())
	}
	return
}

func (mpu *MPU9250) EnableGyroBiasCal(enable bool) (error) {
	enableRegs := []byte{0xb8, 0xaa, 0xb3, 0x8d, 0xb4, 0x98, 0x0d, 0x35, 0x5d}
	disableRegs := []byte{0xb8, 0xaa, 0xaa, 0xaa, 0xb0, 0x88, 0xc3, 0xc5, 0xc7}

	if enable {
		if err := mpu.memWrite(CFG_MOTION_BIAS, &enableRegs); err != nil {
			return errors.New("Unable to enable motion bias compensation")
		}
	} else {
		if err := mpu.memWrite(CFG_MOTION_BIAS, &disableRegs); err != nil {
			return errors.New("Unable to disable motion bias compensation")
		}
	}

		return nil
}

func (mpu *MPU9250) ReadAccelBias(sensAccel byte) error {
	a0x, err := mpu.i2cRead2(MPUREG_XA_OFFSET_H)
	if err != nil {
		return errors.New("ReadAccelBias error reading chip")
	}
	a0y, err := mpu.i2cRead2(MPUREG_YA_OFFSET_H)
	if err != nil {
		return errors.New("ReadAccelBias error reading chip")
	}
	a0z, err := mpu.i2cRead2(MPUREG_ZA_OFFSET_H)
	if err != nil {
		return errors.New("ReadAccelBias error reading chip")
	}

	switch sensAccel {
	case BITS_FS_16G:
		mpu.a01 = a0x >> 1
		mpu.a02 = a0y >> 1
		mpu.a03 = a0z >> 1
	case BITS_FS_4G:
		mpu.a01 = a0x << 1
		mpu.a02 = a0y << 1
		mpu.a03 = a0z << 1
	case BITS_FS_2G:
		mpu.a01 = a0x << 2
		mpu.a02 = a0y << 2
		mpu.a03 = a0z << 2
	default:
		mpu.a01 = a0x
		mpu.a02 = a0y
		mpu.a03 = a0z
	}

	log.Printf("MPU9250 Accel bias read: %d %d %d\n", mpu.a01, mpu.a02, mpu.a03)
	return nil
}

func (mpu *MPU9250) ReadGyroBias(sensGyro byte) error {
	g0x, err := mpu.i2cRead2(MPUREG_XG_OFFS_USRH)
	if err != nil {
		return errors.New("ReadGyroBias error reading chip")
	}
	g0y, err := mpu.i2cRead2(MPUREG_YG_OFFS_USRH)
	if err != nil {
		return errors.New("ReadGyroBias error reading chip")
	}
	g0z, err := mpu.i2cRead2(MPUREG_ZG_OFFS_USRH)
	if err != nil {
		return errors.New("ReadGyroBias error reading chip")
	}

	switch sensGyro {
	case BITS_FS_2000DPS:
		mpu.g01 = g0x >> 1
		mpu.g02 = g0y >> 1
		mpu.g03 = g0z >> 1
	case BITS_FS_500DPS:
		mpu.g01 = g0x << 1
		mpu.g02 = g0y << 1
		mpu.g03 = g0z << 1
	case BITS_FS_250DPS:
		mpu.g01 = g0x << 2
		mpu.g02 = g0y << 2
		mpu.g03 = g0z << 2
	default:
		mpu.g01 = g0x
		mpu.g02 = g0y
		mpu.g03 = g0z
	}

	log.Printf("MPU9250 Gyro  bias read: %d %d %d\n", mpu.g01, mpu.g02, mpu.g03)
	return nil
}

func (mpu *MPU9250) ReadMagCalibration() error {
	// Enable bypass mode
	var tmp uint8
	var err error
	tmp, err = mpu.i2cRead(MPUREG_USER_CTRL)
	if err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}
	if err = mpu.i2cWrite(MPUREG_USER_CTRL, tmp & ^BIT_AUX_IF_EN); err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}
	time.Sleep(3 * time.Millisecond)
	if err = mpu.i2cWrite(MPUREG_INT_PIN_CFG, BIT_BYPASS_EN); err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}

	// Prepare for getting sensitivity data from AK8963
	//Set the I2C slave address of AK8963
	if err = mpu.i2cWrite(MPUREG_I2C_SLV0_ADDR, AK8963_I2C_ADDR); err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}
	// Power down the AK8963
	if err = mpu.i2cWrite(MPUREG_I2C_SLV0_CTRL, AK8963_CNTL1)  ; err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}
	// Power down the AK8963
	if err = mpu.i2cWrite(MPUREG_I2C_SLV0_DO, AKM_POWER_DOWN); err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}
	time.Sleep(time.Millisecond)
	// Fuse AK8963 ROM access
	if mpu.i2cWrite(MPUREG_I2C_SLV0_DO, AK8963_I2CDIS); err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}
	time.Sleep(time.Millisecond)

	// Get sensitivity data from AK8963 fuse ROM
	mcal1, err := mpu.i2cRead(AK8963_ASAX)
	if err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}
	mcal2, err := mpu.i2cRead(AK8963_ASAY)
	if err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}
	mcal3, err := mpu.i2cRead(AK8963_ASAZ)
	if err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}

	mpu.mcal1 = int32(mcal1 + 128)
	mpu.mcal2 = int32(mcal2 + 128)
	mpu.mcal3 = int32(mcal3 + 128)

	// Clean up from getting sensitivity data from AK8963
	// Fuse AK8963 ROM access
	if err = mpu.i2cWrite(MPUREG_I2C_SLV0_DO, AK8963_I2CDIS); err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}
	time.Sleep(time.Millisecond)

	// Disable bypass mode now that we're done getting sensitivity data
	tmp, err = mpu.i2cRead(MPUREG_USER_CTRL)
	if err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}
	if err = mpu.i2cWrite(MPUREG_USER_CTRL, tmp | BIT_AUX_IF_EN); err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}
	time.Sleep(3 * time.Millisecond)
	if err = mpu.i2cWrite(MPUREG_INT_PIN_CFG, 0x00); err != nil {
		return errors.New("ReadMagCalibration error reading chip")
	}

	log.Printf("MPU9250 Mag bias: %d %d %d\n", mpu.mcal1, mpu.mcal2, mpu.mcal3)
	return nil
}

func (mpu *MPU9250) i2cWrite(register, value byte) (err error) {

	if errWrite := mpu.i2cbus.WriteByteToReg(MPU_ADDRESS, register, value); errWrite != nil {
		err = fmt.Errorf("MPU9250 Error writing %X to %X: %s\n",
			value, register, errWrite.Error())
	} else {
		time.Sleep(time.Millisecond)
	}
	return
}

func (mpu *MPU9250) i2cRead(register byte) (value uint8, err error) {
	value, errWrite := mpu.i2cbus.ReadByteFromReg(MPU_ADDRESS, register)
	if errWrite != nil {
		err = fmt.Errorf("i2cRead error: %s", errWrite.Error())
	}
	return
}

func (mpu *MPU9250) i2cRead2(register byte) (value int16, err error) {

	v, errWrite := mpu.i2cbus.ReadWordFromReg(MPU_ADDRESS, register)
	if errWrite != nil {
		err = fmt.Errorf("MPU9250 Error reading %x: %s\n", register, err.Error())
	} else {
		value = int16(v)
	}
	return
}

func (mpu *MPU9250) memWrite(addr uint16, data *[]byte) (error) {
	var err error
	var tmp = make([]byte, 2)

	tmp[0] = byte(addr >> 8)
	tmp[1] = byte(addr & 0xFF)

	// Check memory bank boundaries
	if tmp[1] + byte(len(*data)) > MPU_BANK_SIZE {
		return errors.New("Bad address: writing outside of memory bank boundaries")
	}

	err = mpu.i2cbus.WriteToReg(MPU_ADDRESS, MPUREG_BANK_SEL, tmp)
	if err != nil {
		return fmt.Errorf("MPU9250 Error selecting memory bank: %s\n", err.Error())
	}

	err = mpu.i2cbus.WriteToReg(MPU_ADDRESS, MPUREG_MEM_R_W, *data)
	if err != nil {
		return fmt.Errorf("MPU9250 Error writing to the memory bank: %s\n", err.Error())
	}

	return nil
}
